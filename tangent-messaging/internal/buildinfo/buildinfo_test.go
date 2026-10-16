package buildinfo

import (
	"runtime/debug"
	"testing"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/manifest"
)

func TestResolveBuildIdentity(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	vcs := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}}}
	for _, tt := range []struct {
		name                        string
		info                        *debug.BuildInfo
		version, revision, modified string
		want                        Info
	}{
		{name: "unstamped", want: Info{"0.0.0-dev", "unknown", "unknown"}},
		{name: "tag install", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, want: Info{"0.1.0", "unknown", "unknown"}},
		{name: "commit install", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261016035808-a91aa3954876"}}, want: Info{"0.0.0-20261016035808-a91aa3954876", "unknown", "unknown"}},
		{name: "native vcs", info: vcs, want: Info{"0.0.0-dev+g0123456789ab", revision, "false"}},
		{name: "worktree stamps", revision: revision, modified: "false", want: Info{"0.0.0-dev+g0123456789ab", revision, "false"}},
		{name: "dirty", revision: revision, modified: "true", want: Info{"0.0.0-dev+g0123456789ab.dirty", revision, "true"}},
		{name: "ldflags override", info: vcs, version: "v0.2.0", revision: "fedcba987654", modified: "true", want: Info{"0.2.0", "fedcba987654", "true"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := resolve(tt.info, tt.version, tt.revision, tt.modified)
			if got != tt.want {
				t.Fatalf("identity = %+v, want %+v", got, tt.want)
			}
			if !manifest.ValidVersion(got.Version) {
				t.Fatalf("identity is not an installable manifest version: %q", got.Version)
			}
		})
	}
}
