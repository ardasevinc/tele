// keychain-upgrade-fixture is built into separately signed physical-Mac test
// executables. It is not part of Tele's shipped command surface.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ardasevinc/tele/internal/buildtrust"
	"github.com/ardasevinc/tele/internal/secrets"
)

var buildID = "dev"

var fixtureValues = map[string][]byte{
	"api-id":            []byte("123456"),
	"api-hash":          []byte("fixture-api-hash"),
	"session":           []byte("fixture-session"),
	"manager-bot-token": []byte("fixture-manager-token"),
}

func main() {
	if len(os.Args) != 5 {
		fatal("usage: keychain-upgrade-fixture create|read|read-negative|purge DATA_ROOT PROFILE INSTANCE")
	}
	operation, dataRoot, profile, instance := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch operation {
	case "create":
		requireOfficial()
		store, err := secrets.InitKeychain(ctx, dataRoot, profile, instance)
		must(err)
		defer store.Close()
		for key, value := range fixtureValues {
			must(store.Set(ctx, profile, key, value))
		}
		diagnostics, err := store.CatalogDiagnostics(ctx)
		must(err)
		if diagnostics.Mappings != len(fixtureValues) || diagnostics.Orphans != 0 {
			fatal("unexpected create diagnostics")
		}
		fmt.Printf("tele-keychain-upgrade-create-v1 build=%s mappings=%d generation=%d\n", buildID, diagnostics.Mappings, diagnostics.Generation)
	case "read":
		requireOfficial()
		store, err := secrets.OpenKeychain(ctx, dataRoot, profile, instance)
		must(err)
		defer store.Close()
		for key, want := range fixtureValues {
			got, err := store.Get(ctx, profile, key)
			must(err)
			if string(got) != string(want) {
				fatal("fixture value mismatch")
			}
		}
		diagnostics, err := store.CatalogDiagnostics(ctx)
		must(err)
		if diagnostics.Mappings != len(fixtureValues) || diagnostics.Orphans != 0 {
			fatal("unexpected read diagnostics")
		}
		fmt.Printf("tele-keychain-upgrade-read-v1 build=%s mappings=%d generation=%d\n", buildID, diagnostics.Mappings, diagnostics.Generation)
	case "read-negative":
		store, err := secrets.OpenKeychain(ctx, dataRoot, profile, instance)
		if err == nil {
			defer store.Close()
			_, err = store.Get(ctx, profile, "api-hash")
		}
		if !errors.Is(err, secrets.ErrAccessDenied) && !errors.Is(err, secrets.ErrInteractionRequired) && !errors.Is(err, secrets.ErrInteractionCanceled) && !errors.Is(err, secrets.ErrBackendLocked) {
			if err == nil {
				fatal("differently signed fixture silently read the Keychain instance")
			}
			fatal("negative control returned an unexpected error: " + err.Error())
		}
		fmt.Printf("tele-keychain-upgrade-negative-v1 build=%s result=denied_without_ui\n", buildID)
	case "purge":
		requireOfficial()
		deleted, err := secrets.PurgeKeychain(ctx, dataRoot, profile, instance)
		must(err)
		fmt.Printf("tele-keychain-upgrade-purge-v1 build=%s deleted=%d\n", buildID, deleted)
	default:
		fatal("unknown operation")
	}
}

func requireOfficial() { must(buildtrust.VerifyOfficial()) }

func must(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
