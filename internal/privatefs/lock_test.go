package privatefs

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

func TestWithLockCoordinatesSeparateProcesses(t *testing.T) {
	if os.Getenv("TELE_LOCK_HELPER") == "1" {
		lockPath := os.Getenv("TELE_LOCK_PATH")
		readyPath := os.Getenv("TELE_LOCK_READY")
		releasePath := os.Getenv("TELE_LOCK_RELEASE")
		if err := WithLock(context.Background(), lockPath, func() error {
			if err := os.WriteFile(readyPath, []byte("ready"), FileMode); err != nil {
				return err
			}
			for {
				if _, err := os.Stat(releasePath); err == nil {
					return nil
				}
				time.Sleep(5 * time.Millisecond)
			}
		}); err != nil {
			t.Fatal(err)
		}
		return
	}

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "state.lock")
	readyPath := filepath.Join(dir, "ready")
	releasePath := filepath.Join(dir, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWithLockCoordinatesSeparateProcesses$")
	cmd.Env = append(os.Environ(),
		"TELE_LOCK_HELPER=1",
		"TELE_LOCK_PATH="+lockPath,
		"TELE_LOCK_READY="+readyPath,
		"TELE_LOCK_RELEASE="+releasePath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := WithLock(ctx, lockPath, func() error { return errors.New("must not run") }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cross-process lock error = %v", err)
	}
	if err := os.WriteFile(releasePath, []byte("release"), FileMode); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
