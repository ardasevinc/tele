package peerstore

import (
	"os"
	"path/filepath"
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

func TestStorePersistsPeersDeterministicallyAndPrivately(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, "test")
	if err := store.Save(Cache{Peers: []Peer{
		{Ref: "user:2", Kind: "user", ID: 2, AccessHash: 2},
		{Ref: "user:1", Kind: "user", ID: 1, AccessHash: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	cache, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.Peers) != 2 || cache.Peers[0].Ref != "user:1" || cache.Peers[1].Ref != "user:2" {
		t.Fatalf("peer order = %+v", cache.Peers)
	}
	for target, want := range map[string]os.FileMode{
		filepath.Dir(store.Path()): 0o700,
		store.Path():               0o600,
	} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", target, got, want)
		}
	}
}

func TestResolveMissingPeer(t *testing.T) {
	store := New(t.TempDir(), "test")
	if _, _, err := store.Resolve("missing"); err == nil {
		t.Fatal("Resolve succeeded, want error")
	}
}
