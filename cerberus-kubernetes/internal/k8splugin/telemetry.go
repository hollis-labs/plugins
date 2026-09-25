package k8splugin

import (
	"encoding/json"
	"fmt"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// Telemetry event kinds, declared in plugin.yaml.
const (
	eventChange  = "change"
	eventPreview = "preview"
	eventWarning = "warning"
)

// withChangeTelemetry reports a write's Change to the host's audit record:
// one event per field it changed (or would change, on a dry run), and one per
// warning. The fields are the fixed set Change is built from, so no event can
// carry a credential. Reads report nothing.
func withChangeTelemetry(req subprocess.MCPCallRequest, result subprocess.MCPCallResult) subprocess.MCPCallResult {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok || op.EffectiveEffect().ReadOnly() {
		return result
	}
	var change Change
	if err := json.Unmarshal(result.Content, &change); err != nil {
		return result
	}
	content, err := cerbplugin.AttachTelemetry(result.Content, changeEvents(change)...)
	if err != nil {
		return result
	}
	result.Content = content
	return result
}

func changeEvents(c Change) []cerbplugin.TelemetryEvent {
	kind := eventChange
	if c.DryRun {
		kind = eventPreview
	}
	var events []cerbplugin.TelemetryEvent
	for _, f := range c.Changes {
		events = append(events, cerbplugin.TelemetryEvent{Kind: kind, Message: fmt.Sprintf("%s: %s -> %s", f.Field, f.Before, f.After), Target: c.Target})
	}
	if len(c.Changes) == 0 {
		msg := c.Operation + " changed nothing"
		switch {
		case c.DryRun:
			msg = c.Operation + " previewed; nothing was changed"
		case c.Applied:
			msg = c.Operation + " applied"
		}
		events = append(events, cerbplugin.TelemetryEvent{Kind: kind, Message: msg, Target: c.Target})
	}
	for _, w := range c.Warnings {
		events = append(events, cerbplugin.TelemetryEvent{Kind: eventWarning, Message: w, Target: c.Target})
	}
	return events
}

// telemetryDeclarations names the events every write reports.
func telemetryDeclarations(m contract.Manifest) []cerbplugin.TelemetryDeclaration {
	var out []cerbplugin.TelemetryDeclaration
	for _, op := range m.Operations {
		if op.EffectiveEffect().ReadOnly() {
			continue
		}
		out = append(out, cerbplugin.TelemetryDeclaration{Operation: op.Name, Events: []string{eventChange, eventPreview, eventWarning}})
	}
	return out
}
