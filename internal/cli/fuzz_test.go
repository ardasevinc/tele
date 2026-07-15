package cli

import (
	"strings"
	"testing"
	"time"
)

func FuzzParseTimeFilter(f *testing.F) {
	for _, seed := range []string{"2h", "7d", "2026-07-15", "2026-07-15T12:00:00Z", "", " ", "-2h", "nonsense"} {
		f.Add(seed)
	}
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := parseTimeFilter(value, now)
		if err == nil && parsed.IsZero() && strings.TrimSpace(value) != "" {
			t.Fatalf("successful parse returned zero for %q", value)
		}
	})
}
