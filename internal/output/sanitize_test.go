package output

import (
	"strings"
	"testing"
)

func TestSanitizeTerminalMakesHostileControlsVisible(t *testing.T) {
	input := "safe\x1b[31mred\x1b[0m\r\t\x07\u202espoof\u2069\nnext"
	got := SanitizeTerminal(input)
	for _, want := range []string{"<ESC>[31m", "<CR>", "<TAB>", "<CTRL-U+0007>", "<BIDI-U+202E>", "<BIDI-U+2069>", "\nnext"} {
		if !strings.Contains(got, want) {
			t.Fatalf("SanitizeTerminal missing %q: %q", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\u202e') {
		t.Fatalf("SanitizeTerminal retained active controls: %q", got)
	}
}

func TestSanitizeTerminalMarksInvalidUTF8(t *testing.T) {
	got := SanitizeTerminal(string([]byte{'a', 0xff, 'b'}))
	if got != "a<INVALID-UTF8>b" {
		t.Fatalf("SanitizeTerminal = %q", got)
	}
}

func TestSanitizeTerminalPreservesSecretLikeAndLargeText(t *testing.T) {
	input := "api_hash=looks-secret " + strings.Repeat("x", 1<<20)
	got := SanitizeTerminal(input)
	if got != input {
		t.Fatalf("SanitizeTerminal changed ordinary text: got %d bytes, want %d", len(got), len(input))
	}
}
