package telegram

import (
	"strings"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	scope := scopeFingerprint("history", "user:1", "after:0")
	want := retrievalCursor{
		Kind:          "history",
		Scope:         scope,
		OffsetID:      42,
		OffsetDate:    123,
		OffsetRate:    7,
		OffsetPeerRef: "user:1",
	}
	encoded, err := encodeCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeCursor(encoded, "history", scope)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != cursorVersion || got.Kind != want.Kind || got.Scope != want.Scope || got.OffsetID != want.OffsetID || got.OffsetDate != want.OffsetDate || got.OffsetRate != want.OffsetRate || got.OffsetPeerRef != want.OffsetPeerRef {
		t.Fatalf("decoded cursor = %+v, want %+v", got, want)
	}
}

func TestCursorRejectsWrongScopeAndKind(t *testing.T) {
	encoded, err := encodeCursor(retrievalCursor{Kind: "history", Scope: "scope-a", OffsetID: 42})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind  string
		scope string
	}{
		{kind: "search", scope: "scope-a"},
		{kind: "history", scope: "scope-b"},
	} {
		if _, err := decodeCursor(encoded, test.kind, test.scope); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("decodeCursor(%q, %q) error = %v", test.kind, test.scope, err)
		}
	}
}

func TestCursorRejectsMalformedInput(t *testing.T) {
	if _, err := decodeCursor("not-base64!", "history", "scope"); err == nil {
		t.Fatal("decodeCursor accepted malformed input")
	}
}

func TestValidateRequestedLimit(t *testing.T) {
	if err := validateRequestedLimit(1000, maxMessageResults); err != nil {
		t.Fatal(err)
	}
	if err := validateRequestedLimit(1001, maxMessageResults); err == nil {
		t.Fatal("validateRequestedLimit accepted an over-limit request")
	}
}
