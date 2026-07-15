package telegram

import (
	"path/filepath"
	"strings"
	"testing"
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

func FuzzLoginCodeRedaction(f *testing.F) {
	for _, seed := range []string{"Login code: 12345", "This is your login code:\nABCDE", "ordinary text", "\x1b[31m"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		otherText, otherRedactions := redactMessageText("user:42", text)
		if otherText != text || len(otherRedactions) != 0 {
			t.Fatalf("non-service message changed: %q -> %q (%v)", text, otherText, otherRedactions)
		}
		redacted, labels := redactMessageText("user:777000", text)
		if redacted == text && len(labels) != 0 {
			t.Fatalf("unchanged text has redaction receipt: %v", labels)
		}
		if redacted != text && (len(labels) != 1 || labels[0] != "telegram_login_code") {
			t.Fatalf("changed text lacks exact receipt: %v", labels)
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
