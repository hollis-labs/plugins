package forgeplugin

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

// redactedMarker replaces a scrubbed value. It matches the host redactor's
// marker so an operator sees one convention whichever side did the work.
const redactedMarker = "[REDACTED]"

// minScrubLength keeps a degenerate credential (a stray character from a bad
// secret reference) from turning every error into a field of markers.
const minScrubLength = 4

// scrubber removes the credential values this plugin holds from text before it
// crosses the plugin boundary.
//
// This is redaction at the value boundary: the plugin knows the exact token it
// was handed, so it can remove that token wherever it appears, including from
// error text composed by net/http or echoed by Forge that no pattern could recognise. A
// plugin cannot import Cerberus's internal/redact, so this is a deliberately
// small local copy of the one thing needed. The host's own redaction still runs
// over everything afterwards; this is the part only the plugin can do.
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
		// A value can surface verbatim or encoded into a URL an SDK prints.
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if !seen[form] {
				seen[form] = true
				values = append(values, form)
			}
		}
	}
	// Longest first, so an encoded form is not half-replaced by a shorter one.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return scrubber{values: values}
}

func (s scrubber) text(text string) string {
	for _, value := range s.values {
		text = strings.ReplaceAll(text, value, redactedMarker)
	}
	return text
}

// err returns an error whose text has had every held value removed. The chain
// is not preserved: nothing above this plugin inspects it, and an unwrapped
// cause would still carry the original text.
func (s scrubber) err(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(s.text(err.Error()))
}
