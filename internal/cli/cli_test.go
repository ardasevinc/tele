package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
