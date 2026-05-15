package cli

import (
	"bytes"
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
