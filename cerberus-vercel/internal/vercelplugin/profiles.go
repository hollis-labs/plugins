package vercelplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile is one deployment profile. The file's shape is the one Cerberus's
// own infra.yaml used (a top-level profiles: list with these keys), so a file
// written for it reads here unchanged. Keys this plugin does not use — the
// DNS and git provider fields, labels, providers: — are ignored: labels come
// from the config.yaml resource with the profile's id, never from this file.
type Profile struct {
	ID               string `yaml:"id" json:"id"`
	Name             string `yaml:"name" json:"name,omitempty"`
	Provider         string `yaml:"provider" json:"provider,omitempty"`
	RepoPath         string `yaml:"repo_path" json:"repo_path"`
	Domain           string `yaml:"domain" json:"domain,omitempty"`
	GitRemote        string `yaml:"git_remote" json:"git_remote,omitempty"`
	VercelProject    string `yaml:"vercel_project" json:"vercel_project,omitempty"`
	VercelScope      string `yaml:"vercel_scope" json:"vercel_scope,omitempty"`
	PreflightCommand string `yaml:"preflight_command" json:"preflight_command,omitempty"`
	BuildCommand     string `yaml:"build_command" json:"build_command,omitempty"`
	DeployCommand    string `yaml:"deploy_command" json:"deploy_command,omitempty"`
}

type profilesFile struct {
	Profiles []Profile `yaml:"profiles"`
}

// errNoProfilesFile is the refusal when the operator has not said where the
// profiles are. It is worded to survive the host's redact.Text: no
// "name: value" or "name=value" shapes and no flag followed by a word.
var errNoProfilesFile = errors.New("no profiles file is configured. Set the profiles_file field under vercel in connector-config.yaml, " +
	"then reload the plugin with `cerberus connectors plugin managed load vercel`")

// expandHome resolves a leading ~/ against the home directory.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// loadProfiles reads the profiles file. It is read on every call, so an edit
// takes effect without a reload.
func loadProfiles(path string) ([]Profile, error) {
	if path == "" {
		return nil, errNoProfilesFile
	}
	data, err := os.ReadFile(path) //nolint:gosec // the operator's own file, named in connector-config.yaml
	if err != nil {
		return nil, fmt.Errorf("read profiles file %s (%w)", path, errors.Unwrap(err))
	}
	var file profilesFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse profiles file %s (%v)", path, err)
	}
	seen := map[string]bool{}
	for i := range file.Profiles {
		p := &file.Profiles[i]
		p.ID = strings.TrimSpace(p.ID)
		if p.ID == "" {
			return nil, fmt.Errorf("profiles file %s has a profile with no id (entry %d)", path, i+1)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("profiles file %s has two profiles with the id %q", path, p.ID)
		}
		seen[p.ID] = true
	}
	return file.Profiles, nil
}

func findProfile(profiles []Profile, id string) (Profile, bool) {
	for _, p := range profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// digest is the profile's sha256 over its canonical JSON. The preview carries
// it, so an approval made against one profile is stale for an edited one.
func (p Profile) digest() string {
	data, _ := json.Marshal(p)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (p Profile) summary() ProfileSummary {
	return ProfileSummary{
		ID: p.ID, Name: p.Name, RepoPath: p.RepoPath, VercelProject: p.VercelProject,
		VercelScope: p.VercelScope, Domain: p.Domain, Linked: hasVercelLink(p.RepoPath),
	}
}

func hasVercelLink(repoPath string) bool {
	if repoPath == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(repoPath, ".vercel", "project.json"))
	return err == nil
}
