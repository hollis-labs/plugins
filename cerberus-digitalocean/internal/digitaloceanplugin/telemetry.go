package digitaloceanplugin

import (
	"strconv"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// Telemetry event kinds this plugin reports, declared in plugin.yaml so the
// install review can say which operations' audit records carry the plugin's
// view as well as the host's.
const (
	eventChange  = "change"
	eventPreview = "preview"
)

// withOperationTelemetry attaches what a successful non-read operation did
// to its result, for the host's audit record: a change, or a preview when it
// was a dry run, naming the target from the operation's declared target
// fields. The host strips it before the result reaches any caller. Reads
// report nothing.
func withOperationTelemetry(req subprocess.MCPCallRequest, result subprocess.MCPCallResult) subprocess.MCPCallResult {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok || op.EffectiveEffect().ReadOnly() {
		return result
	}
	event := cerbplugin.TelemetryEvent{Kind: eventChange, Message: op.Name + " completed", Target: telemetryTarget(op, req.Arguments)}
	// Only an operation with a preview runs dry; the plugin refuses the rest.
	if dry, _ := req.Arguments[argDryRun].(bool); dry && op.EffectivePreview() != contract.PreviewNone {
		event = cerbplugin.TelemetryEvent{Kind: eventPreview, Message: op.Name + " previewed; nothing was changed", Target: event.Target}
	}
	content, err := cerbplugin.AttachTelemetry(result.Content, event)
	if err != nil {
		return result
	}
	result.Content = content
	return result
}

// telemetryTarget names the operation's target the way the host's audit
// record does: its declared target fields, as key=value.
func telemetryTarget(op contract.ManifestOperation, args map[string]any) string {
	var parts []string
	for _, field := range op.Target.From {
		if value, ok := args[field]; ok {
			if s := stringOf(value); s != "" {
				parts = append(parts, field+"="+s)
			}
		}
	}
	if len(parts) == 0 {
		return op.Target.Kind
	}
	return strings.Join(parts, ",")
}

func stringOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}
