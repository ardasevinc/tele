package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/tele/internal/output"
	tgapp "github.com/ardasevinc/tele/internal/telegram"
)

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
	if got != "[profile test] sent user:1 #42" {
		t.Fatalf("mutationReceipt = %q", got)
	}
}

func TestWriteTranscript(t *testing.T) {
	var out bytes.Buffer
	state := &appState{out: &out}
	meta := output.Meta{
		Profile:     "main",
		PeerRef:     "user:1",
		FetchedAt:   "2026-05-15T11:25:34Z",
		Limit:       50,
		SideEffects: []string{"may_mark_read"},
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
		"side_effects: may_mark_read",
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
