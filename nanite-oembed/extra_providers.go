package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/hollis-labs/plugin-sdk/manifest"
)

type extraProvider struct {
	Name     string `json:"name"`
	Pattern  string `json:"pattern"`
	Endpoint string `json:"endpoint"`
}

func parseExtraProviders(blob string) ([]*Provider, error) {
	if len(blob) > 64<<10 {
		return nil, fmt.Errorf("provider configuration exceeds limit")
	}
	// The shared strict decoder accepts objects; wrap the user-facing array so
	// duplicate keys, unknown fields and trailing input receive the same checks.
	var config struct {
		Providers []extraProvider `json:"providers"`
	}
	if err := manifest.DecodeExtension([]byte(`{"providers":`+blob+`}`), &config); err != nil {
		return nil, fmt.Errorf("providers must be a JSON array of name, pattern and endpoint triples")
	}
	entries := config.Providers
	if entries == nil || len(entries) > 32 {
		return nil, fmt.Errorf("providers must be an array with at most 32 entries")
	}
	names := map[string]bool{}
	providers := make([]*Provider, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == "" || len(entry.Name) > 128 || names[entry.Name] || entry.Pattern == "" || len(entry.Pattern) > 4096 || strings.Count(entry.Endpoint, "{url}") != 1 || len(entry.Endpoint) > 8192 {
			return nil, fmt.Errorf("provider requires a unique name, bounded pattern and endpoint with one {url}")
		}
		names[entry.Name] = true
		pattern, err := regexp.Compile(entry.Pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid provider pattern")
		}
		endpoint, err := url.Parse(strings.Replace(entry.Endpoint, "{url}", url.QueryEscape("https://example.invalid/"), 1))
		if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Fragment != "" {
			return nil, fmt.Errorf("invalid provider endpoint")
		}
		providers = append(providers, &Provider{Name: entry.Name, URLPatterns: []*regexp.Regexp{pattern}, Endpoint: entry.Endpoint})
	}
	return providers, nil
}
func validContentURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Hostname() != "" && parsed.User == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && len(raw) <= 8192
}
func matchProviderIn(providers []*Provider, raw string) *Provider {
	if !validContentURL(raw) {
		return nil
	}
	for _, provider := range providers {
		for _, pattern := range provider.URLPatterns {
			if pattern.MatchString(raw) {
				return provider
			}
		}
	}
	return nil
}
