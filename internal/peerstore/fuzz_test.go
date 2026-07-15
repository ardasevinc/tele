package peerstore

import (
	"strings"
	"testing"
)

func FuzzNormalizeToken(f *testing.F) {
	for _, seed := range []string{"@arda", "  user:42  ", "@@channel", "", "@ @"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		got := normalizeToken(token)
		if got != strings.TrimSpace(got) {
			t.Fatalf("normalization retained outer whitespace: %q", got)
		}
		if strings.HasPrefix(got, "@") {
			t.Fatalf("normalization retained @ prefix: %q", got)
		}
		if normalizeToken(got) != got {
			t.Fatalf("normalization is not idempotent: %q", got)
		}
	})
}
