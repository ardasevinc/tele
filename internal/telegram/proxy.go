package telegram

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/telegram/dcs"
)

// proxyEnvKeys lists the proxy variables in precedence order. The sandbox
// egress tunnel address is a bare host:port and is treated as http://.
var proxyEnvKeys = []string{
	"TELE_HTTP_PROXY",
	"HTTPS_PROXY", "https_proxy",
	"HTTP_PROXY", "http_proxy",
	"SAND_EGRESS_TUNNEL_PROXY_ADDR",
}

// proxyFromEnv returns the first configured HTTP CONNECT proxy, or nil.
func proxyFromEnv(lookup func(string) string) (*url.URL, error) {
	for _, key := range proxyEnvKeys {
		raw := strings.TrimSpace(lookup(key))
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", key, err)
		}
		if u.Scheme != "http" {
			return nil, fmt.Errorf("unsupported %s scheme %q; only http:// CONNECT proxies are supported", key, u.Scheme)
		}
		if u.Hostname() == "" || u.Port() == "" {
			return nil, fmt.Errorf("invalid %s: expected host:port", key)
		}
		return u, nil
	}
	return nil, nil
}

// mtprotoResolver routes MTProto dials through an HTTP CONNECT proxy when one
// is configured in the environment, and otherwise keeps gotd's default.
func mtprotoResolver() (dcs.Resolver, error) {
	proxy, err := proxyFromEnv(os.Getenv)
	if err != nil || proxy == nil {
		return nil, err
	}
	return dcs.Plain(dcs.PlainOptions{Dial: connectDialer(proxy)}), nil
}

// connectDialer returns a dial function that opens a TCP tunnel to addr through
// the given HTTP proxy using the CONNECT method.
func connectDialer(proxy *url.URL) dcs.DialFunc {
	return func(ctx context.Context, network, addr string) (_ net.Conn, rerr error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, proxy.Host)
		if err != nil {
			return nil, fmt.Errorf("dial proxy %s: %w", proxy.Host, err)
		}
		defer func() {
			if rerr != nil {
				_ = conn.Close()
			}
		}()
		if deadline, ok := ctx.Deadline(); ok {
			if err := conn.SetDeadline(deadline); err != nil {
				return nil, err
			}
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()

		req := &http.Request{
			Method: http.MethodConnect,
			URL:    &url.URL{Opaque: addr},
			Host:   addr,
			Header: make(http.Header),
		}
		if user := proxy.User; user != nil {
			password, _ := user.Password()
			token := base64.StdEncoding.EncodeToString([]byte(user.Username() + ":" + password))
			req.Header.Set("Proxy-Authorization", "Basic "+token)
		}
		if err := req.Write(conn); err != nil {
			return nil, fmt.Errorf("proxy CONNECT %s: %w", addr, err)
		}
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("proxy CONNECT %s: %w", addr, err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("proxy CONNECT %s: %s", addr, resp.Status)
		}
		if br.Buffered() > 0 {
			return nil, fmt.Errorf("proxy CONNECT %s: unexpected data after response", addr)
		}
		if !stop() {
			return nil, ctx.Err()
		}
		if err := conn.SetDeadline(time.Time{}); err != nil {
			return nil, err
		}
		return conn, nil
	}
}
