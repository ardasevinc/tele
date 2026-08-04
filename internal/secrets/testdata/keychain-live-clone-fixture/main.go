// keychain-live-clone-fixture copies a retained vault snapshot into an already
// initialized disposable Keychain instance for the physical Homebrew upgrade
// proof. It is not part of Tele's shipped command surface.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ardasevinc/tele/internal/buildtrust"
	"github.com/ardasevinc/tele/internal/privatefs"
	"github.com/ardasevinc/tele/internal/secrets"
)

func main() {
	if len(os.Args) != 9 {
		fatal("usage: keychain-live-clone-fixture SOURCE_VAULT SOURCE_PROFILE SOURCE_INSTANCE PASSPHRASE_FILE SOURCE_SESSION TARGET_DATA TARGET_PROFILE TARGET_INSTANCE")
	}
	if err := buildtrust.VerifyOfficial(); err != nil {
		fatal(err.Error())
	}
	sourceVault, sourceProfile, sourceInstance := os.Args[1], os.Args[2], os.Args[3]
	passphraseFile, sourceSession := os.Args[4], os.Args[5]
	targetData, targetProfile, targetInstance := os.Args[6], os.Args[7], os.Args[8]
	if !strings.HasPrefix(targetProfile, "homebrew-proof-") || targetProfile == sourceProfile {
		fatal("target must be a distinct homebrew-proof-* profile")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	passphrase, err := secrets.ReadPassphraseFile(passphraseFile)
	must(err)
	defer zero(passphrase)
	source, err := secrets.OpenVault(sourceVault, sourceProfile, sourceInstance, passphrase)
	must(err)
	defer source.Close()
	snapshot, err := source.Snapshot(ctx)
	must(err)
	defer func() {
		for _, value := range snapshot {
			zero(value)
		}
	}()

	target, err := secrets.OpenKeychain(ctx, targetData, targetProfile, targetInstance)
	must(err)
	defer target.Close()
	diagnostics, err := target.CatalogDiagnostics(ctx)
	must(err)
	if diagnostics.Mappings != 0 || diagnostics.Orphans != 0 {
		fatal("target Keychain instance is not empty")
	}
	for key, value := range snapshot {
		must(target.Set(ctx, targetProfile, key, value))
	}

	sessionBytes, err := os.ReadFile(sourceSession)
	must(err)
	defer zero(sessionBytes)
	targetSession := filepath.Join(targetData, targetProfile, "session.enc")
	must(privatefs.AtomicWriteFile(targetSession, sessionBytes))
	diagnostics, err = target.CatalogDiagnostics(ctx)
	must(err)
	if diagnostics.Mappings != len(snapshot) || diagnostics.Orphans != 0 {
		fatal("target Keychain catalog does not match the source snapshot")
	}
	fmt.Printf("tele-keychain-live-clone-v1 keys=%d session_bytes=%d generation=%d\n", len(snapshot), len(sessionBytes), diagnostics.Generation)
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func must(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
