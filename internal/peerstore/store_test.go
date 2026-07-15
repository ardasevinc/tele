package peerstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	if err := store.Upsert(context.Background(), []Peer{peer}); err != nil {
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

func TestUpsertMergesConcurrentWriters(t *testing.T) {
	store := New(t.TempDir(), "test")
	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.Upsert(context.Background(), []Peer{{
				Ref: fmt.Sprintf("user:%d", i+1), Kind: "user", ID: int64(i + 1), AccessHash: int64(i + 100),
			}})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cache, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.Peers) != workers {
		t.Fatalf("peers = %d, want %d", len(cache.Peers), workers)
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

func TestLoadRejectsCorruptCache(t *testing.T) {
	store := New(t.TempDir(), "test")
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load accepted corrupt peer cache")
	}
}
