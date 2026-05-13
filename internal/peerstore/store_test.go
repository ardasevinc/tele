package peerstore

import (
	"testing"
)

func TestStoreRoundTripAndResolve(t *testing.T) {
	store := New(t.TempDir(), "test")
	peer := Peer{
		Ref:        "user:42",
		Kind:       "user",
		ID:         42,
		AccessHash: 99,
		Title:      "Ada Lovelace",
		Username:   "ada",
	}
	if err := store.Upsert([]Peer{peer}); err != nil {
		t.Fatal(err)
	}
	input, got, err := store.Resolve("@ada")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != peer.Ref {
		t.Fatalf("ref = %q, want %q", got.Ref, peer.Ref)
	}
	if input.TypeName() != "inputPeerUser" {
		t.Fatalf("input type = %q, want inputPeerUser", input.TypeName())
	}
}

func TestResolveMissingPeer(t *testing.T) {
	store := New(t.TempDir(), "test")
	if _, _, err := store.Resolve("missing"); err == nil {
		t.Fatal("Resolve succeeded, want error")
	}
}
