package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolvePreservesStampedReleaseIdentity(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.1.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "metadata-commit"},
		},
	}

	version, commit := resolve("1.1.0", "stamped-commit", info)
	if version != "1.1.0" || commit != "stamped-commit" {
		t.Fatalf("resolve() = %q, %q", version, commit)
	}
}

func TestResolveUsesVCSIdentityForCheckoutBuild(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "checkout-commit"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	version, commit := resolve("1.1.0", "dev", info)
	if version != "1.1.0" || commit != "checkout-commit-dirty" {
		t.Fatalf("resolve() = %q, %q", version, commit)
	}
}

func TestResolveUsesModuleIdentityForGoInstall(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.1.0"}}

	version, commit := resolve("stale-source-version", "dev", info)
	if version != "1.1.0" || commit != "module v1.1.0" {
		t.Fatalf("resolve() = %q, %q", version, commit)
	}
}

func TestResolveLeavesUnstampedBuildWithoutMetadataAsDevelopment(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}

	version, commit := resolve("1.1.0", "dev", info)
	if version != "1.1.0" || commit != "dev" {
		t.Fatalf("resolve() = %q, %q", version, commit)
	}
}
