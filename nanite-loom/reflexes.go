package main

import (
	"encoding/json"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

// These predicates are constructed in this module and must fit the public DSL.
func predicate(value map[string]interface{}) pluginapi.ReflexPredicate {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var result pluginapi.ReflexPredicate
	if err = json.Unmarshal(raw, &result); err != nil {
		panic(err)
	}
	return result
}

const naniteWikiTopicPattern = `(?i)\b(envelope|boot[- ]?profile|mcp trust|durable[- ]?agent|plugin (architecture|system|sdk)|slot system|context broker|tool[- ]result cache|agent_profiles|reflex(es)?|wiki|loom curator|loom weaver)\b`

// discoveryLanguagePattern anchors capture_on_discovery's text match to
// language that signals a agent has just surfaced something worth
// keeping, as named in loom-architecture.md §8 / CW-20260816-0023's
// brief ("discovered", "found that", "turns out", "worth documenting",
// "TIL").
const discoveryLanguagePattern = `(?i)\b(discovered|found that|turns out|worth documenting|worth capturing|worth keeping|noteworthy|TIL)\b`

// wikiOrLoomToolPattern matches the wiki_*/loom_* MCP tool-name family
// check_before_answer uses to confirm the agent hasn't already
// consulted the bundle this window.
const wikiOrLoomToolPattern = `^(wiki_|loom_)`

func loomReflexSeeds() []pluginapi.ReflexSeed {
	checkBeforeAnswerTrigger := map[string]interface{}{
		"kind": "AND",
		"clauses": []interface{}{
			map[string]interface{}{
				"kind":    "text_regex_window",
				"scope":   "user",
				"window":  2,
				"pattern": naniteWikiTopicPattern,
			},
			map[string]interface{}{
				"kind":    "tool_name_window",
				"window":  2,
				"mode":    "none",
				"pattern": wikiOrLoomToolPattern,
			},
		},
	}

	captureOnDiscoveryTrigger := map[string]interface{}{
		"kind": "AND",
		"clauses": []interface{}{
			map[string]interface{}{
				"kind":    "regex_match_window",
				"window":  2,
				"pattern": discoveryLanguagePattern,
			},
			map[string]interface{}{
				"kind":   "tool_calls_window",
				"window": 2,
				"op":     ">",
				"value":  0,
			},
		},
	}

	return []pluginapi.ReflexSeed{
		{
			AgentSlug: "loom-weaver",
			ID:        "check-before-answer",

			Priority: 60,
			Trigger:  predicate(checkBeforeAnswerTrigger),

			Reminder: "Reflex check_before_answer: the recent question touches Nanite wiki-bundle topics and wiki_*/loom_* tools haven't been called yet this window. Search the nanite bundle via loom_page_search (then loom_page_get / loom_fetch_result / loom_search_result as needed) before answering from memory — per your own answer_with_sources procedure.",
		},
		{
			AgentSlug: "loom-weaver",
			ID:        "weaver-capture-on-discovery",

			Priority: 55,
			Trigger:  predicate(captureOnDiscoveryTrigger),

			Reminder: "Reflex capture_on_discovery: recent output reads like a fresh finding backed by real tool activity. If this surfaced a gap, staleness, or contradiction worth keeping in the nanite wiki bundle, drop a concise note into Fragments Engine's inbox now via relay_*/message_* (affected page, source refs, observed gap, confidence) so Loom Curator's next pass can act on it — per your own propose_kb_update procedure.",
		},
		{
			AgentSlug: "loom-curator",
			ID:        "curator-capture-on-discovery",

			Priority: 55,
			Trigger:  predicate(captureOnDiscoveryTrigger),

			Reminder: "Reflex capture_on_discovery: recent output reads like a fresh finding backed by real tool activity, outside the normal classify/compile flow. If it's a wiki-worthy observation not already covered by write_or_stage_page's staging step or scheduled_lint_and_export's inbox routing, drop a concise note into Fragments Engine's inbox now via relay_*/message_* so it isn't lost.",
		},
	}
}
