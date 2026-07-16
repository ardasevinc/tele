package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolvePreservesStampedReleaseIdentity(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.0.2"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "metadata-commit"},
		},
	}

	version, commit := resolve("1.0.2", "stamped-commit", info)
	if version != "1.0.2" || commit != "stamped-commit" {
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

	version, commit := resolve("1.0.2", "dev", info)
	if version != "1.0.2" || commit != "checkout-commit-dirty" {
		t.Fatalf("resolve() = %q, %q", version, commit)
	}
}

func TestResolveUsesModuleIdentityForGoInstall(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.0.2"}}

	version, commit := resolve("stale-source-version", "dev", info)
	if version != "1.0.2" || commit != "module v1.0.2" {
		t.Fatalf("resolve() = %q, %q", version, commit)
	}
}

func TestResolveLeavesUnstampedBuildWithoutMetadataAsDevelopment(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}

	version, commit := resolve("1.0.2", "dev", info)
	if version != "1.0.2" || commit != "dev" {
		t.Fatalf("resolve() = %q, %q", version, commit)
	}
}
