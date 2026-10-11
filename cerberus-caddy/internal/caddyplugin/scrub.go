package caddyplugin

import (
	"net/url"
	"sort"
	"strings"
)

// redactedMarker matches the host redactor's marker.
const redactedMarker = "[REDACTED]"

// minScrubLength keeps a degenerate secret from turning every message into a
// field of markers.
const minScrubLength = 4

// scrubber removes the credential values this plugin holds from text before it
// crosses the plugin boundary. The plugin knows the exact token it was handed,
// so it can remove it wherever it appears, including in caddy's own output,
// which no pattern could recognise. The host's redaction still runs afterwards.
type scrubber struct {
	values []string
}

func newScrubber(secrets ...string) scrubber {
	seen := map[string]bool{}
	var values []string
	for _, secret := range secrets {
		if len(secret) < minScrubLength {
			continue
		}
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if !seen[form] {
				seen[form] = true
				values = append(values, form)
			}
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return scrubber{values: values}
}

func (s scrubber) text(text string) string {
	for _, value := range s.values {
		text = strings.ReplaceAll(text, value, redactedMarker)
	}
	return text
}
