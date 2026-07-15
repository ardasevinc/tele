package telegram

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func FuzzSafeDownloadFileName(f *testing.F) {
	for _, seed := range []string{"photo.jpg", "../escape", `..\\escape`, "a:b/c", "\x00", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got := safeDownloadFileName(42, name)
		if !strings.HasPrefix(got, "42-") {
			t.Fatalf("missing message prefix: %q", got)
		}
		if got != filepath.Base(got) || strings.ContainsAny(got, "/\\:\x00") {
			t.Fatalf("unsafe filename: %q", got)
		}
	})
}

func FuzzMessageContentPreservation(f *testing.F) {
	for _, seed := range []string{"Login code: 12345", "This is your login code:\nABCDE", "ordinary text", "\x1b[31m"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		message := &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 777000}, Message: text}
		message.SetFlags()
		messages, _ := convertMessages("", &tg.MessagesMessages{Messages: []tg.MessageClass{message}})
		if len(messages) != 1 || messages[0].Text != text {
			t.Fatalf("message content changed: %q -> %#v", text, messages)
		}
	})
}

func FuzzCursorDecode(f *testing.F) {
	valid, err := encodeCursor(retrievalCursor{Kind: "history", Scope: "scope", OffsetID: 42})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{"", valid, "%%%", "e30"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		cursor, err := decodeCursor(value, "history", "scope")
		if err == nil && (cursor.Version != cursorVersion || cursor.Kind != "history" || cursor.Scope != "scope") {
			t.Fatalf("accepted mismatched cursor: %+v", cursor)
		}
	})
}
