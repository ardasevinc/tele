package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

const (
	vaultProcessWriterEnv     = "TELE_TEST_VAULT_PROCESS_WRITER"
	vaultProcessWriterPathEnv = "TELE_TEST_VAULT_PROCESS_WRITER_PATH"
	vaultProcessWriterIDEnv   = "TELE_TEST_VAULT_PROCESS_WRITER_ID"
)

const testVaultInstance = "8e34c2c8-9c20-4cb4-ae66-e63ee0f3be50"

func TestVaultRoundTripAndSnapshot(t *testing.T) {
	path := filepath.Join(secureTempDir(t), "main", "secrets", testVaultInstance+".vault")
	passphrase := []byte("correct horse battery staple")
	created, err := CreateVault(context.Background(), path, "main", testVaultInstance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer created.Close()

	if err := created.Set(context.Background(), "main", "api-hash", []byte("secret hash")); err != nil {
		t.Fatal(err)
	}
	if err := created.Set(context.Background(), "main", "bot:42", []byte{0, 1, 2, 255}); err != nil {
		t.Fatal(err)
	}
	created.Close()

	opened, err := OpenVault(path, "main", testVaultInstance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	got, err := opened.Get(context.Background(), "main", "api-hash")
	if err != nil || string(got) != "secret hash" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	snapshot, err := opened.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 2 || !bytes.Equal(snapshot["bot:42"], []byte{0, 1, 2, 255}) {
		t.Fatalf("Snapshot = %#v", snapshot)
	}
	snapshot["api-hash"][0] = 'X'
	got, err = opened.Get(context.Background(), "main", "api-hash")
	if err != nil || string(got) != "secret hash" {
		t.Fatal("Snapshot exposed mutable vault state")
	}
	if err := opened.Delete(context.Background(), "main", "api-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.Get(context.Background(), "main", "api-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("vault mode = %o, want 600", info.Mode().Perm())
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("vault directory mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestVaultWrongPassphraseAndAuthenticatedTamperAreIndistinguishable(t *testing.T) {
	path, passphrase := createTestVault(t)
	if _, err := OpenVault(path, "main", testVaultInstance, []byte("wrong")); !errors.Is(err, ErrVaultUnlockFailed) {
		t.Fatalf("wrong passphrase error = %v", err)
	}

	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{90, vaultHeaderSize} {
		tampered := append([]byte(nil), original...)
		tampered[offset] ^= 0x01
		if err := os.WriteFile(path, tampered, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenVault(path, "main", testVaultInstance, passphrase); !errors.Is(err, ErrVaultUnlockFailed) {
			t.Fatalf("tamper at %d error = %v", offset, err)
		}
	}
}

func TestVaultRejectsStructureBeforeKDF(t *testing.T) {
	path, passphrase := createTestVault(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func([]byte){
		"reserved": func(data []byte) { data[13] = 1 },
		"argon memory ceiling": func(data []byte) {
			binary.BigEndian.PutUint32(data[46:50], vaultMaxArgonMemory+1)
		},
		"payload length": func(data []byte) { binary.BigEndian.PutUint32(data[164:168], 1) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), original...)
			mutate(data)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenVault(path, "main", testVaultInstance, passphrase); !errors.Is(err, ErrVaultCorrupt) {
				t.Fatalf("OpenVault error = %v, want ErrVaultCorrupt", err)
			}
		})
	}
}

func TestVaultRejectsFutureVersionWithoutMutation(t *testing.T) {
	path, passphrase := createTestVault(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(data[8:10], vaultFormatVersion+1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(data)
	if _, err := OpenVault(path, "main", testVaultInstance, passphrase); !errors.Is(err, ErrVaultVersionUnsupported) {
		t.Fatalf("OpenVault error = %v", err)
	}
	afterData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if after := sha256.Sum256(afterData); after != before {
		t.Fatal("future-version vault was mutated")
	}
}

func TestVaultCanBeRelocated(t *testing.T) {
	path, passphrase := createTestVault(t)
	destinationDir := secureTempDir(t)
	if err := os.Chmod(destinationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(destinationDir, "restored.vault")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenVault(destination, "main", testVaultInstance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
}

func TestVaultRejectsSymlink(t *testing.T) {
	path, passphrase := createTestVault(t)
	link := filepath.Join(secureTempDir(t), "vault-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(link, "main", testVaultInstance, passphrase); !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("OpenVault symlink error = %v", err)
	}
}

func TestVaultRejectsSymlinkedParent(t *testing.T) {
	realDir := filepath.Join(secureTempDir(t), "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(secureTempDir(t), "linked")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(linkDir, testVaultInstance+".vault")
	if _, err := CreateVault(context.Background(), path, "main", testVaultInstance, []byte("passphrase")); !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("CreateVault error = %v, want ErrVaultCorrupt", err)
	}
}

func TestVaultConcurrentWritersPreserveAllRecords(t *testing.T) {
	path, passphrase := createTestVault(t)
	store, err := OpenVault(path, "main", testVaultInstance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const writers = 20
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := "key-" + string(rune('a'+i))
			errs <- store.Set(context.Background(), "main", key, []byte(key))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != writers {
		t.Fatalf("record count = %d, want %d", len(snapshot), writers)
	}
}

func TestVaultSeparateProcessWritersPreserveAllRecords(t *testing.T) {
	if child := os.Getenv(vaultProcessWriterEnv); child != "" {
		path := os.Getenv(vaultProcessWriterPathEnv)
		writerID := os.Getenv(vaultProcessWriterIDEnv)
		if path == "" || writerID == "" {
			t.Fatal("invalid child parameters")
		}
		store, err := OpenVault(path, "main", testVaultInstance, []byte("process writer passphrase"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		for i := 0; i < 8; i++ {
			key := fmt.Sprintf("%s-%d", writerID, i)
			if err := store.Set(context.Background(), "main", key, []byte(key)); err != nil {
				t.Fatal(err)
			}
		}
		return
	}

	path := filepath.Join(secureTempDir(t), "vault")
	store, err := CreateVault(context.Background(), path, "main", testVaultInstance, []byte("process writer passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	commands := make([]*exec.Cmd, 3)
	outputs := make([]bytes.Buffer, len(commands))
	for i := range commands {
		child := exec.Command(os.Args[0], "-test.run=^TestVaultSeparateProcessWritersPreserveAllRecords$")
		child.Env = append(os.Environ(),
			vaultProcessWriterEnv+"=1",
			vaultProcessWriterPathEnv+"="+path,
			vaultProcessWriterIDEnv+"="+fmt.Sprintf("writer-%d", i),
		)
		child.Stdout = &outputs[i]
		child.Stderr = &outputs[i]
		commands[i] = child
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, child := range commands {
		if err := child.Wait(); err != nil {
			t.Fatalf("child writer failed: %v\n%s", err, outputs[i].Bytes())
		}
	}

	store, err = OpenVault(path, "main", testVaultInstance, []byte("process writer passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != len(commands)*8 {
		t.Fatalf("record count = %d, want %d", len(snapshot), len(commands)*8)
	}
}

func TestVaultStoreUsesProfileWideLockReentrantly(t *testing.T) {
	path, passphrase := createTestVault(t)
	store, err := OpenVault(path, "main", testVaultInstance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lockPath := filepath.Join(filepath.Dir(path), "profile.lock")
	if err := withProfileLockPath(context.Background(), lockPath, func(ctx context.Context) error {
		if err := store.Set(ctx, "main", "key", []byte("value")); err != nil {
			return err
		}
		_, err := store.Snapshot(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRejectDuplicateJSONKeys(t *testing.T) {
	if err := rejectDuplicateJSONKeys([]byte(`{"schema":1,"schema":1}`)); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := rejectDuplicateJSONKeys([]byte(`{"records":{"a":"","a":""}}`)); err == nil {
		t.Fatal("nested duplicate key accepted")
	}
	if err := rejectDuplicateJSONKeys([]byte(`{"schema":1,"records":{"a":""}}`)); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
}

func TestVaultHeaderLayout(t *testing.T) {
	path, _ := createTestVault(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data[:8], []byte{'T', 'E', 'L', 'E', 'V', 'L', 'T', 0}) {
		t.Fatalf("magic = %q", data[:8])
	}
	if binary.BigEndian.Uint16(data[8:10]) != 1 || data[10] != 1 || data[11] != 1 || data[12] != 1 {
		t.Fatalf("wire identifiers = %x", data[8:13])
	}
	if len(data[82:130]) != 48 || binary.BigEndian.Uint16(data[130:132]) != 1 {
		t.Fatal("wire offsets changed")
	}
}

func TestVaultV1CompatibilityVector(t *testing.T) {
	const fixture = "VEVMRVZMVAAAAQEBAQCONMLInCBMtK5m5j7g875QICEiIyQlJicoKSorLC0uLwABAAAAAAADAQAAADAxMjM0NTY3ODk6Ozw9Pj9AQUJDREVGR7IWGPKPd4MVWgGiipL/tx+8yhCOBlmY78t1PFEhIWnnajq4OxFFj3Ns6I480OdIIgABAAAAAAAAAAFISUpLTE1OT1BRUlNUVVZXWFlaW1xdXl8AAAB9yZx/o+SHIi+BkfzOZAO4ZNn19kNFTXdFZbh9xVUyeW4CeldDz/3VhjuN9rE54IshxDKbmTK/JEIG8BzFLKQ4XsTGV5ISqWh3VtJCbk+HlD8azdY1QjzzlOhV1cFV8bFH18ltIaHJDVRkjpGUJQm4FISfgMGdS7Rqtv6OmUc="
	encoded, err := base64.StdEncoding.DecodeString(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(secureTempDir(t), testVaultInstance+".vault")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenVault(path, "main", testVaultInstance, []byte("compatibility passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 0 {
		t.Fatalf("fixture snapshot = %#v", snapshot)
	}
}

func TestVaultDiagnosticsExposeMetadataNotValues(t *testing.T) {
	path, passphrase := createTestVault(t)
	store, err := OpenVault(path, "main", testVaultInstance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Set(context.Background(), "main", "api-hash", []byte("SUPERSECRET")); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := store.VaultDiagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.FormatVersion != 1 || diagnostics.PayloadSchema != 1 || diagnostics.Generation != 2 || diagnostics.Records != 1 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if diagnostics.ArgonMemoryKiB != 64*1024 || diagnostics.ArgonIterations != 3 || diagnostics.ArgonParallelism != 1 {
		t.Fatalf("Argon diagnostics = %+v", diagnostics)
	}
}

func TestReadPassphraseFileRejectsUnsafeMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPassphraseFile(path); err == nil {
		t.Fatal("unsafe passphrase file was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := ReadPassphraseFile(path)
	if err != nil || string(value) != "secret" {
		t.Fatalf("ReadPassphraseFile = %q, %v", value, err)
	}
}

func TestReadPassphraseFileRejectsSymlinkAndOversize(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "credential")
	if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "credential-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPassphraseFile(link); err == nil {
		t.Fatal("symlinked passphrase file was accepted")
	}
	oversized := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte{'x'}, MaxPassphraseSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPassphraseFile(oversized); err == nil {
		t.Fatal("oversized passphrase file was accepted")
	}
}

func TestReadPassphraseFDClosesDescriptor(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.Write([]byte("secret\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	value, err := ReadPassphraseFD(int(read.Fd()))
	if err != nil || string(value) != "secret" {
		t.Fatalf("ReadPassphraseFD = %q, %v", value, err)
	}
	if _, err := read.Read(make([]byte, 1)); err == nil {
		t.Fatal("passphrase descriptor remained open")
	}
}

func FuzzVaultHeaderParsing(f *testing.F) {
	f.Add([]byte("TELEVAULT"))
	f.Add(make([]byte, vaultHeaderSize))
	instanceID, err := parseUUID(testVaultInstance)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseVaultHeader(data, instanceID)
	})
}

func FuzzRejectDuplicateJSONKeys(f *testing.F) {
	f.Add([]byte(`{"schema":1,"records":{}}`))
	f.Add([]byte(`{"schema":1,"schema":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = rejectDuplicateJSONKeys(data)
	})
}

func createTestVault(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(secureTempDir(t), "vault", testVaultInstance+".vault")
	passphrase := []byte("portable vault test passphrase")
	store, err := CreateVault(context.Background(), path, "main", testVaultInstance, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	return path, passphrase
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
