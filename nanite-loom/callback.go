package main

import (
	"fmt"
	"strings"
)

type LoomCuratorWakeFragment struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	SourceType    string `json:"source_type"`
	SourceID      string `json:"source_id"`
	Title         string `json:"title"`
	CanonicalPath string `json:"canonical_path"`
}

// LoomCuratorWakeRequest is the Fragments Engine callback identity payload.
type LoomCuratorWakeRequest struct {
	Generator string                  `json:"generator"`
	Fragment  LoomCuratorWakeFragment `json:"fragment"`
}

func buildLoomCuratorWakePrompt(generator string, frag LoomCuratorWakeFragment) string {
	var b strings.Builder
	b.WriteString("Fragments Engine routed a fragment into the `nanite` wiki bundle via a callback wake")
	if generator != "" {
		fmt.Fprintf(&b, " (generator: %s)", generator)
	}
	b.WriteString(".\n\nRun your classify_and_compile_fragment procedure now. This payload carries no body text by design — fetch the fragment's real content yourself via fragment_id before classifying. Fragment identity:\n")
	fmt.Fprintf(&b, "- fragment_id: %s\n", frag.ID)
	if frag.Source != "" {
		fmt.Fprintf(&b, "- fragment_source: %s\n", frag.Source)
	}
	if frag.SourceType != "" {
		fmt.Fprintf(&b, "- fragment_source_type: %s\n", frag.SourceType)
	}
	if frag.SourceID != "" {
		fmt.Fprintf(&b, "- fragment_source_id: %s\n", frag.SourceID)
	}
	if frag.Title != "" {
		fmt.Fprintf(&b, "- fragment_title: %s\n", frag.Title)
	}
	if frag.CanonicalPath != "" {
		fmt.Fprintf(&b, "- fragment_canonical_path: %s\n", frag.CanonicalPath)
	}
	return b.String()
}
