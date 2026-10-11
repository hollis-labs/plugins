package digitaloceanplugin

import (
	"context"
	"fmt"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The fleet operations (firewalls, volumes, snapshots, keys, tags, projects,
// reserved IPs, VPCs) are served from a table rather than plugin.go's switch.
// Each file registers its handlers and its contract in init, and registering a
// write puts it in ackOperations and previewOperations, so the acknowledgment
// and dry-run gates in call() cover it without a second list to keep in step.

// fleetHandler runs one operation. dryRun is already parsed and, for a write,
// the acknowledgment already checked. A handler that gets dryRun returns a
// preview before it touches p.backend, so a preview needs no credential.
type fleetHandler func(p *Plugin, ctx context.Context, a *argMap, dryRun bool) (any, error)

var (
	fleetHandlers = map[string]fleetHandler{}
	fleetContract []contract.Operation
)

// registerFleet adds an operation. A write is acknowledgment-gated and has a
// preview; a read has neither.
func registerFleet(op contract.Operation, write bool, h fleetHandler) {
	if _, dup := fleetHandlers[op.Name]; dup {
		panic("duplicate fleet operation " + op.Name)
	}
	fleetHandlers[op.Name] = h
	fleetContract = append(fleetContract, op)
	if write {
		ackOperations[op.Name] = true
		previewOperations[op.Name] = true
	}
}

// fleetOperations returns the registered contracts in a stable order: the
// order the files register them, which is the order their init functions run
// (file name order), then registration order within a file.
func fleetOperations() []contract.Operation {
	return append([]contract.Operation(nil), fleetContract...)
}

// readOp and writeOp fill in the fields every fleet operation shares.
func readOp(name, targetKind string, targetFrom []string, desc string, out map[string]any, input map[string]any, example string) contract.Operation {
	return contract.Operation{
		Name:         name,
		OutputSchema: out,
		Effect:       contract.EffectRead,
		Target:       contract.TargetDescriptor{Kind: targetKind, From: targetFrom},
		Preview:      contract.PreviewNone,
		Output:       contract.OutputStructured,
		Cost:         contract.CostNone,
		LocalFS:      contract.LocalFSNone,
		Description:  desc,
		InputSchema:  input,
		Examples:     []string{"cerberus connectors exec digitalocean " + example},
	}
}

func writeOp(name string, effect contract.Effect, cost contract.Cost, targetKind string, targetFrom []string, desc string, out map[string]any, input map[string]any, example string) contract.Operation {
	return contract.Operation{
		Name:         name,
		OutputSchema: out,
		Effect:       effect,
		Reversible:   effect != contract.EffectDestructive,
		Target:       contract.TargetDescriptor{Kind: targetKind, From: targetFrom},
		Preview:      contract.PreviewPlugin,
		Output:       contract.OutputStructured,
		Cost:         cost,
		LocalFS:      contract.LocalFSNone,
		Description:  desc,
		InputSchema:  input,
		Examples: []string{
			"cerberus connectors exec digitalocean " + name + " " + example + " --dry-run",
			"cerberus connectors exec digitalocean " + name + " " + example + " --ack",
		},
	}
}

// callFleet dispatches a registered operation. ok is false when name is not one.
func (p *Plugin) callFleet(ctx context.Context, name string, a *argMap, dryRun bool) (result any, ok bool, err error) {
	h, ok := fleetHandlers[name]
	if !ok {
		return nil, false, nil
	}
	result, err = h(p, ctx, a, dryRun)
	return result, true, err
}

// strProp, idProp and friends build input-schema properties.
func strProp(desc string) map[string]any { return contract.StringSchema(desc) }
func intProp(desc string) map[string]any { return contract.IntegerSchema(desc) }
func listProp(desc string, item string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": item}}
}

func idsDetail(ids []int) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = fmt.Sprint(id)
	}
	return out
}
