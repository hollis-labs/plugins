// Package buildinfo reports the identity embedded in this plugin binary.
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Version, Revision and Modified may be set with go build -ldflags -X.
// Empty values fall back to Go build metadata; absent provenance stays unknown.
var Version, Revision, Modified string

type Info struct {
	Version  string
	Revision string
	Modified string
}

func Current() Info {
	info, _ := debug.ReadBuildInfo()
	return resolve(info, Version, Revision, Modified)
}

func resolve(info *debug.BuildInfo, version, revision, modified string) Info {
	if info != nil {
		if version == "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if revision == "" {
					revision = setting.Value
				}
			case "vcs.modified":
				if modified == "" {
					modified = setting.Value
				}
			}
		}
	}
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		version = "0.0.0-dev"
		if revision != "" {
			short := revision
			if len(short) > 12 {
				short = short[:12]
			}
			version += "+g" + short
			if modified == "true" {
				version += ".dirty"
			}
		}
	}
	if revision == "" {
		revision = "unknown"
	}
	if modified != "true" && modified != "false" {
		modified = "unknown"
	}
	return Info{Version: version, Revision: revision, Modified: modified}
}

// HealthMessage keeps the operational status first and adds build provenance.
// The SDK health wire supports a message, not arbitrary metadata fields.
func (i Info) HealthMessage(status string) string {
	return fmt.Sprintf("%s version=%s vcs.revision=%s vcs.modified=%s", status, i.Version, i.Revision, i.Modified)
}
