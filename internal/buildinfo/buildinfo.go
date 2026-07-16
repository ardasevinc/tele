package buildinfo

import (
	"runtime/debug"
	"strings"
)

var (
	Version = "1.0.2"
	Commit  = "dev"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	Version, Commit = resolve(Version, Commit, info)
}

func resolve(version, commit string, info *debug.BuildInfo) (string, string) {
	moduleVersion := strings.TrimPrefix(info.Main.Version, "v")
	if commit != "" && commit != "dev" {
		return version, commit
	}

	revision := ""
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision != "" {
		if dirty {
			revision += "-dirty"
		}
		return version, revision
	}
	if moduleVersion != "" && moduleVersion != "(devel)" {
		return moduleVersion, "module " + info.Main.Version
	}
	return version, commit
}
