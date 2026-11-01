package telegram

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestProxyFromEnvPrecedence(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "none", env: map[string]string{}, want: ""},
		{name: "sandbox bare host:port", env: map[string]string{"SAND_EGRESS_TUNNEL_PROXY_ADDR": "127.0.0.1:8791"}, want: "http://127.0.0.1:8791"},
		{name: "https over sandbox", env: map[string]string{"HTTPS_PROXY": "http://a:1", "SAND_EGRESS_TUNNEL_PROXY_ADDR": "127.0.0.1:8791"}, want: "http://a:1"},
		{name: "tele wins", env: map[string]string{"TELE_HTTP_PROXY": "b:2", "HTTPS_PROXY": "http://a:1"}, want: "http://b:2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := proxyFromEnv(func(k string) string { return tt.env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if gotStr := urlString(got); gotStr != tt.want {
				t.Fatalf("proxy = %q, want %q", gotStr, tt.want)
			}
		})
	}
	if _, err := proxyFromEnv(func(k string) string {
		if k == "HTTPS_PROXY" {
			return "socks5://127.0.0.1:1080"
		}
		return ""
	}); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func urlString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}

func TestConnectDialerTunnels(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	gotTarget := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil || req.Method != http.MethodConnect {
			return
		}
		gotTarget <- req.Host + " " + req.Header.Get("Proxy-Authorization")
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		_, _ = io.Copy(conn, br) // echo tunnel bytes back
	}()

	proxy := &url.URL{Scheme: "http", Host: ln.Addr().String(), User: url.UserPassword("u", "p")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := connectDialer(proxy)(ctx, "tcp", "149.154.167.50:443")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if got, want := <-gotTarget, "149.154.167.50:443 Basic dTpw"; got != want {
		t.Fatalf("CONNECT = %q, want %q", got, want)
	}
	payload := []byte{0xee, 0xee, 0xee, 0xee, 0x00, 0x01}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo = %x, want %x", buf, payload)
	}
}

func TestConnectDialerRejectsNon200(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = http.ReadRequest(bufio.NewReader(conn))
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := connectDialer(&url.URL{Scheme: "http", Host: ln.Addr().String()})(ctx, "tcp", "1.2.3.4:443"); err == nil {
		t.Fatal("expected error for 403")
	}
}
