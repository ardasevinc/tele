package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/tele/internal/config"
	"github.com/ardasevinc/tele/internal/output"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestParseTimeFilterDuration(t *testing.T) {
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	got, err := parseTimeFilter("2h", now)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(-2 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("parseTimeFilter = %s, want %s", got, want)
	}
}

func TestExitCodeUsesWrappedStableFamily(t *testing.T) {
	err := exitError{code: output.ExitInvalidInput, err: errors.New("bad input")}
	if got := ExitCode(err); got != output.ExitInvalidInput {
		t.Fatalf("ExitCode = %d, want %d", got, output.ExitInvalidInput)
	}
}

func TestParseTimeFilterDays(t *testing.T) {
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	got, err := parseTimeFilter("7d", now)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(-7 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("parseTimeFilter = %s, want %s", got, want)
	}
}

func TestParsePositiveInt(t *testing.T) {
	if got, err := parsePositiveInt("123", "msg-id"); err != nil || got != 123 {
		t.Fatalf("parsePositiveInt = %d, %v; want 123, nil", got, err)
	}
	if _, err := parsePositiveInt("0", "msg-id"); err == nil {
		t.Fatal("parsePositiveInt accepted zero")
	}
}

func TestReadOnlyRejectsMutation(t *testing.T) {
	state := &appState{readOnly: true}
	err := state.requireWritable("send")
	if err == nil || err.Error() != "send is disabled by --read-only" {
		t.Fatalf("requireWritable error = %v", err)
	}
}

func TestReadOnlyAllowsDryRun(t *testing.T) {
	state := &appState{readOnly: true, dryRun: true}
	if err := state.requireWritable("send"); err != nil {
		t.Fatalf("requireWritable rejected dry run: %v", err)
	}
}

func TestReadOnlyGuardsEveryMutationCommand(t *testing.T) {
	tests := [][]string{
		{"--read-only", "send", "user:1", "--text", "hello"},
		{"--read-only", "reply", "user:1", "1", "--text", "hello"},
		{"--read-only", "react", "user:1", "1", "--emoji", "👍"},
		{"--read-only", "edit", "user:1", "1", "--text", "hello"},
		{"--read-only", "delete", "user:1", "1", "--for-me", "--yes"},
	}
	for _, args := range tests {
		t.Run(args[1], func(t *testing.T) {
			state := &appState{in: strings.NewReader(""), out: &bytes.Buffer{}, err: &bytes.Buffer{}}
			cmd := rootCommand(context.Background(), state)
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), "disabled by --read-only") {
				t.Fatalf("execute error = %v", err)
			}
		})
	}
}

func TestJSONAndJSONLAreMutuallyExclusive(t *testing.T) {
	state := &appState{in: strings.NewReader(""), out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	cmd := rootCommand(context.Background(), state)
	cmd.SetArgs([]string{"--json", "--jsonl", "me"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("execute error = %v", err)
	}
}

func TestPublicConfigOmitsPhone(t *testing.T) {
	view := publicConfig(config.Config{
		DefaultLimit:   50,
		DefaultProfile: "main",
		Profiles: map[string]config.Profile{
			"main": {APIID: 123, Phone: "+905555555555"},
		},
	})
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("905555555555")) || bytes.Contains(b, []byte("phone")) {
		t.Fatalf("public config leaked phone: %s", b)
	}
	if !bytes.Contains(b, []byte(`"api_id":123`)) {
		t.Fatalf("public config omitted api id: %s", b)
	}
}

func TestPublicAuthViewsOmitPhoneAndPendingCodeHash(t *testing.T) {
	status := publicAuthStatus(tgapp.AuthStatus{
		Profile:    "main",
		Authorized: true,
		Account:    &tgapp.Account{ID: 42, Username: "arda", Phone: "+905555555555"},
	})
	start := publicAuthStart(tgapp.AuthStartStatus{
		Profile:        "main",
		Phone:          "+905555555555",
		CodeSent:       true,
		CodeType:       "app",
		TimeoutSeconds: 60,
	})
	for name, value := range map[string]any{"status": status, "start": start} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("905555555555")) || bytes.Contains(b, []byte("phone")) || bytes.Contains(b, []byte("code_hash")) {
			t.Fatalf("public auth %s leaked private auth material: %s", name, b)
		}
	}
}

func TestConfigGetUsesMachineEnvelopeAndTypedJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.Config{
		DefaultLimit:   50,
		DefaultProfile: "main",
		Profiles:       map[string]config.Profile{"main": {APIID: 123, Phone: "+905555555555"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		flag      string
		wantLines int
	}{
		{name: "json", flag: "--json"},
		{name: "jsonl", flag: "--jsonl", wantLines: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			state := &appState{in: strings.NewReader(""), out: &out, err: &bytes.Buffer{}}
			cmd := rootCommand(context.Background(), state)
			cmd.SetArgs([]string{"--config", path, test.flag, "config", "get"})
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(out.Bytes(), []byte("905555555555")) || bytes.Contains(out.Bytes(), []byte(`"Phone"`)) {
				t.Fatalf("config get leaked phone: %s", out.String())
			}
			if test.flag == "--json" {
				var envelope map[string]any
				if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
					t.Fatalf("invalid JSON output: %v", err)
				}
				if envelope["schema_version"] != output.SchemaVersion {
					t.Fatalf("schema_version = %v", envelope["schema_version"])
				}
				return
			}
			lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
			if len(lines) != test.wantLines {
				t.Fatalf("output has %d lines, want %d:\n%s", len(lines), test.wantLines, out.String())
			}
			for _, line := range lines {
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatalf("invalid machine line %q: %v", line, err)
				}
				if record["schema_version"] != output.SchemaVersion {
					t.Fatalf("schema_version = %v", record["schema_version"])
				}
			}
		})
	}
}

func TestMutationReceiptIncludesProfile(t *testing.T) {
	state := &appState{profile: "test"}
	got := state.mutationReceipt("sent user:1 #42")
	if got != "[profile test] confirmed: sent user:1 #42" {
		t.Fatalf("mutationReceipt = %q", got)
	}
}

func TestWriteMutationResultPreservesConfirmedOutcomeOnOutputFailure(t *testing.T) {
	state := &appState{out: failingWriter{}, err: &bytes.Buffer{}}
	result := tgapp.MutationResult{
		OK:                   true,
		Outcome:              tgapp.MutationConfirmed,
		ReconciliationHandle: "random_id:42",
	}
	err := writeMutationResult(state, result, output.NewMeta("test"), "confirmed")
	var mutationErr tgapp.MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("writeMutationResult error = %T, want MutationError", err)
	}
	if mutationErr.Outcome != tgapp.MutationConfirmed || mutationErr.RetrySafe {
		t.Fatalf("writeMutationResult error = %+v", mutationErr)
	}
}

func TestRetrievalSummaryReportsUnknownCompleteness(t *testing.T) {
	meta := output.Meta{Retrieval: &output.RetrievalMeta{
		RequestedCount: 100,
		ReturnedCount:  50,
		Complete:       nil,
		Truncated:      true,
		NextCursor:     "cursor-1",
		Pages:          25,
	}}
	got := retrievalSummary(meta)
	for _, want := range []string{"requested=100", "returned=50", "complete=unknown", "truncated=true", "pages=25", "next_cursor=cursor-1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("retrievalSummary missing %q: %s", want, got)
		}
	}
}

func TestWriteTranscript(t *testing.T) {
	var out bytes.Buffer
	state := &appState{out: &out}
	meta := output.Meta{
		Profile:   "main",
		PeerRef:   "user:1",
		FetchedAt: "2026-05-15T11:25:34Z",
		Limit:     50,
	}
	messages := []tgapp.Message{
		{
			ID:       10,
			Date:     "2026-05-13T12:01:53Z",
			Text:     "hello\nsecond line",
			Outgoing: false,
		},
		{
			ID:       11,
			Date:     "2026-05-13T12:02:53Z",
			Media:    "messageMediaPhoto",
			Outgoing: true,
		},
	}
	peer := tgapp.PeerInfo{Ref: "user:1", Title: "Hakan abi", Username: "hakankozakli"}
	if err := writeTranscript(state, messages, meta, peer); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"peer: user:1 (Hakan abi @hakankozakli)",
		"fetched_at: 2026-05-15T11:25:34Z",
		"messages: 2",
		"-- 2026-05-13 --",
		"[10] 12:01 them: hello",
		"    second line",
		"[11] 12:02 me: [photo]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q:\n%s", want, got)
		}
	}
}

func TestWriteTranscriptRendersResolvedGroupSpeaker(t *testing.T) {
	var out bytes.Buffer
	state := &appState{out: &out}
	meta := output.Meta{Profile: "main", PeerRef: "supergroup:20", FetchedAt: "2026-05-15T11:25:34Z"}
	messages := []tgapp.Message{{ID: 10, Date: "2026-05-13T12:01:53Z", Text: "hello", SenderPeerRef: "user:10", SenderLabel: "Alice @alice"}}
	if err := writeTranscript(state, messages, meta, tgapp.PeerInfo{Ref: "supergroup:20", Title: "Builders"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[10] 12:01 Alice @alice: hello") {
		t.Fatalf("transcript did not render sender:\n%s", out.String())
	}
}

func TestHumanMessageRenderingMakesTerminalControlsVisible(t *testing.T) {
	var out bytes.Buffer
	state := &appState{out: &out}
	message := tgapp.Message{
		ID:          10,
		Date:        "2026-05-13T12:01:53Z",
		Text:        "first\x1b[31m\nsecond\u202Eline",
		SenderLabel: "Ali\tce",
	}
	if err := writeMessages(state, []tgapp.Message{message}, output.Meta{}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Ali<TAB>ce", "first<ESC>[31m", "\n    second<BIDI-U+202E>line"} {
		if !strings.Contains(got, want) {
			t.Fatalf("human output missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\u202E') {
		t.Fatalf("human output retained terminal controls: %q", got)
	}
}

func TestJSONMessageRenderingPreservesExactContent(t *testing.T) {
	var out bytes.Buffer
	state := &appState{out: &out, json: true}
	original := "first\x1b[31m\nsecond\u202Eline"
	if err := writeMessages(state, []tgapp.Message{{ID: 10, Text: original}}, output.Meta{}); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data []tgapp.Message `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].Text != original {
		t.Fatalf("JSON message text = %q, want exact %q", envelope.Data[0].Text, original)
	}
}
