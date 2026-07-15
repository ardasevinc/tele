package privatefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestWithLockSerializesReadModifyWrite(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "state.lock")
	valuePath := filepath.Join(dir, "value")
	if err := AtomicWriteFile(valuePath, []byte("0")); err != nil {
		t.Fatal(err)
	}
	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- WithLock(context.Background(), lockPath, func() error {
				b, err := os.ReadFile(valuePath)
				if err != nil {
					return err
				}
				value, err := strconv.Atoi(string(b))
				if err != nil {
					return err
				}
				return AtomicWriteFile(valuePath, []byte(strconv.Itoa(value+1)))
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(valuePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "20" {
		t.Fatalf("value = %q, want 20", b)
	}
}

func TestWithLockHonorsCancellation(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "state.lock")
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithLock(context.Background(), lockPath, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := WithLock(ctx, lockPath, func() error { return errors.New("must not run") })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
