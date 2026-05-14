package cli

import (
	"testing"
	"time"
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
