package k8splugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// Plugin serves the Kubernetes connector over the plugin-sdk subprocess
// protocol.
type Plugin struct {
	backend Backend
	config  subprocess.ConfigReader
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against a live cluster.
func New() *Plugin { return &Plugin{backend: NewClientGoBackend()} }

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin { return &Plugin{backend: backend} }

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers the value
	// with the SDK logger's redaction tracker, so a later log line carrying the
	// token as a field writes REDACTED instead of the token.
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus Kubernetes Connector",
		Version:     def.Version,
		Description: "Kubernetes cluster inspection and administration for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load never fails on a missing credential. That is the point: when
// authentication is broken, list_contexts and check_access are the two
// operations an operator needs, and a plugin that refused to load would take
// them away at exactly the moment they matter.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	if p.backend == nil {
		p.backend = NewClientGoBackend()
	}
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error { return nil }

func (p *Plugin) Health(ctx context.Context) (subprocess.HealthStatus, error) {
	if p.backend == nil {
		return subprocess.HealthStatus{OK: false, Message: "not loaded"}, nil
	}
	health, err := p.backend.Health(ctx, p.clusterOptions(nil))
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: err.Error()}, nil
	}
	if !health.Reachable {
		return subprocess.HealthStatus{OK: false, Message: health.Message}, nil
	}
	return subprocess.HealthStatus{
		OK:      true,
		Message: "cluster reachable at " + health.Server + " running " + health.Version,
	}, nil
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	if p.backend == nil {
		return subprocess.MCPCallResult{}, fmt.Errorf("kubernetes plugin is not loaded")
	}
	args := arguments(req.Arguments)
	opts := p.clusterOptions(args)

	switch req.ToolName {
	case tool("list_contexts"):
		if override := args.str("kubeconfig"); override != "" {
			opts.Kubeconfig = override
		}
		return marshalResult(Contexts(opts))

	case tool("check_access"):
		if override := args.str("kubeconfig"); override != "" {
			opts.Kubeconfig = override
		}
		return marshalResult(Preflight(opts))

	case tool("get_health"):
		return marshalResult(p.backend.Health(ctx, opts))

	case tool("list_namespaces"):
		query := p.listQuery(args)
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Namespaces(ctx, opts, query))

	case tool("list_nodes"):
		query := p.listQuery(args)
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Nodes(ctx, opts, query))

	case tool("list_pods"):
		query := PodQuery{
			Namespace:     p.namespace(args, opts),
			AllNamespaces: args.boolean("all_namespaces"),
			Selector:      args.str("selector"),
			Limit:         args.intOr("limit", DefaultListLimit),
			Continue:      args.str("continue"),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Pods(ctx, opts, query))

	case tool("list_workloads"):
		query := WorkloadQuery{
			Namespace:     p.namespace(args, opts),
			AllNamespaces: args.boolean("all_namespaces"),
			Kind:          args.str("kind"),
			Limit:         args.intOr("limit", DefaultListLimit),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Workloads(ctx, opts, query))

	case tool("list_events"):
		query := EventQuery{
			Namespace:     p.namespace(args, opts),
			AllNamespaces: args.boolean("all_namespaces"),
			Limit:         args.intOr("limit", DefaultEventLimit),
			Continue:      args.str("continue"),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Events(ctx, opts, query))

	case tool("get_logs"):
		pod := args.str("pod")
		if pod == "" {
			return subprocess.MCPCallResult{}, fmt.Errorf("get_logs requires a pod name")
		}
		query := LogQuery{
			Namespace: p.namespace(args, opts),
			Pod:       pod,
			Container: args.str("container"),
			Tail:      args.intOr("tail", DefaultLogTail),
			Previous:  args.boolean("previous"),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Logs(ctx, opts, query))

	case tool("describe_workload"):
		ref := WorkloadRef{Namespace: p.namespace(args, opts), Kind: args.required("kind"), Name: args.required("name")}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.DescribeWorkload(ctx, opts, ref))

	case tool("list_services"):
		query := p.scopedQuery(args, opts)
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Services(ctx, opts, query))

	case tool("list_ingresses"):
		query := p.scopedQuery(args, opts)
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Ingresses(ctx, opts, query))

	case tool("list_api_resources"):
		query := APIResourceQuery{
			Group: args.str("group"),
			Limit: args.intOr("limit", DefaultAPIResourceLimit),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.APIResources(ctx, opts, query))

	case tool("top"):
		query := TopQuery{
			Kind:          args.str("kind"),
			Namespace:     p.namespace(args, opts),
			AllNamespaces: args.boolean("all_namespaces"),
			Selector:      args.str("selector"),
			Limit:         args.intOr("limit", DefaultListLimit),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Top(ctx, opts, query))

	case tool("scale_workload"):
		ref := WorkloadRef{Namespace: p.namespace(args, opts), Kind: args.required("kind"), Name: args.required("name")}
		replicas := args.requiredInt("replicas")
		dryRun := p.writeMode(args, "scale_workload")
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Scale(ctx, opts, ScaleRequest{WorkloadRef: ref, Replicas: int32(replicas), DryRun: dryRun}))

	case tool("restart_workload"):
		ref := WorkloadRef{Namespace: p.namespace(args, opts), Kind: args.required("kind"), Name: args.required("name")}
		dryRun := p.writeMode(args, "restart_workload")
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Restart(ctx, opts, RestartRequest{WorkloadRef: ref, DryRun: dryRun}))

	case tool("cordon_node"), tool("uncordon_node"):
		schedulable := req.ToolName == tool("uncordon_node")
		operation := "cordon_node"
		if schedulable {
			operation = "uncordon_node"
		}
		node := args.required("node")
		dryRun := p.writeMode(args, operation)
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.SetSchedulable(ctx, opts, SchedulableRequest{Node: node, Schedulable: schedulable, DryRun: dryRun}))

	case tool("delete_pod"):
		request := DeletePodRequest{Namespace: p.namespace(args, opts), Name: args.required("pod")}
		if args.has("grace_period_seconds") {
			grace := int64(args.intOr("grace_period_seconds", 0))
			request.GracePeriod = &grace
		}
		request.DryRun = p.writeMode(args, "delete_pod")
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.DeletePod(ctx, opts, request))

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
}

// writeOperations is the one list of operations that change a cluster. The
// manifest marks each Destructive and SupportsDry, and
// TestWriteOperationsAreExactlyTheDestructiveOnes holds the two in step, so a
// write cannot be added without the host's --ack gate applying to it.
var writeOperations = map[string]bool{
	"scale_workload":   true,
	"restart_workload": true,
	"cordon_node":      true,
	"uncordon_node":    true,
	"delete_pod":       true,
}

// Argument keys the host adds to a call itself, from --dry-run and --ack. They
// are not in any operation's input schema because the caller does not set
// them as operation arguments; the host does.
const (
	argDryRun       = "dry_run"
	argAcknowledged = "acknowledged"
)

// writeMode reads the dry-run flag for a write and refuses a real write that
// arrives unacknowledged.
//
// The host already refuses a Destructive operation without --ack before the
// call reaches this process, so under the host this check never fires. It is
// here for the binary driven any other way — directly over the protocol, or by
// a host whose policy differs — so that the plugin's own contract is "no
// unacknowledged write", not "no unacknowledged write provided the caller
// checked". A dry run needs no acknowledgment from the plugin's side: it
// changes nothing.
func (p *Plugin) writeMode(args *argMap, operation string) bool {
	dryRun := args.boolean(argDryRun)
	if !dryRun && !args.boolean(argAcknowledged) {
		args.problems = append(args.problems, operation+" changes the cluster and requires acknowledgment: re-run it acknowledged (--ack), or preview it first with --dry-run")
	}
	return dryRun
}

func (p *Plugin) scopedQuery(args *argMap, opts ClusterOptions) ScopedQuery {
	return ScopedQuery{
		Namespace:     p.namespace(args, opts),
		AllNamespaces: args.boolean("all_namespaces"),
		Selector:      args.str("selector"),
		Limit:         args.intOr("limit", DefaultListLimit),
		Continue:      args.str("continue"),
	}
}

func tool(operation string) string {
	return cerbplugin.ToolNameForOperation(ConnectorID, operation)
}

// clusterOptions resolves a connection per call: host-supplied config first,
// then the operation's own override. Selection is per operation rather than per
// process so one daemon can serve several clusters and the next call cannot
// inherit this one's context.
func (p *Plugin) clusterOptions(args *argMap) ClusterOptions {
	opts := ClusterOptions{
		Kubeconfig:     p.configString(ConfigKubeconfig),
		Context:        p.configString(ConfigContext),
		Server:         p.configString(ConfigServer),
		CredentialPath: p.configString(ConfigCredentialPath),
		Token:          p.token(),
	}
	if args != nil {
		if override := args.str("context"); override != "" {
			opts.Context = override
		}
	}
	return opts
}

func (p *Plugin) listQuery(args *argMap) ListQuery {
	return ListQuery{
		Limit:    args.intOr("limit", DefaultListLimit),
		Continue: args.str("continue"),
	}
}

// namespace resolves the namespace for a namespaced operation, in the order
// kubectl would plus this connector's own setting: the operation's argument,
// the connector's configured namespace, the kubeconfig context's namespace,
// then "default".
func (p *Plugin) namespace(args *argMap, opts ClusterOptions) string {
	if ns := args.str("namespace"); ns != "" {
		return ns
	}
	if ns := p.configString(ConfigNamespace); ns != "" {
		return ns
	}
	if ns := ContextNamespace(opts); ns != "" {
		return ns
	}
	return DefaultNamespace
}

func (p *Plugin) configString(key string) string {
	if p.config == nil {
		return ""
	}
	return p.config.String(key)
}

// token reads the bearer token the host resolved on this plugin's behalf,
// falling back to the environment only for a binary run directly, outside the
// host, which receives no init config. The plugin never reaches a credential
// store itself.
func (p *Plugin) token() string {
	if p.config != nil {
		if token := p.config.Secret(SecretToken); token != "" {
			return token
		}
	}
	return os.Getenv(TokenEnvVar)
}

// argMap reads operation arguments, which arrive with two different typings
// depending on which surface the caller used — and getting this wrong is
// silent, not loud.
//
// Over MCP the arguments are decoded JSON, so a number is a float64 and a
// boolean is a bool. Over the CLI they arrive from `--arg key=value`, so
// *everything* is a string. A parser that handled only the JSON types read
// `limit=2` as absent and fell back to the default, and read
// `all_namespaces=true` as false — an operator asking for a cluster-wide read
// got one namespace back with no indication the flag had been dropped.
//
// Hence two rules here. Accept both typings, and **fail loudly on a value that
// cannot be parsed**: a malformed limit silently becoming the default is the
// same bug wearing a different hat.
type argMap struct {
	raw      map[string]any
	problems []string
}

func arguments(raw map[string]any) *argMap {
	if raw == nil {
		raw = map[string]any{}
	}
	return &argMap{raw: raw}
}

// err reports every argument problem at once, so a caller fixes one call rather
// than discovering the next fault on the next attempt.
func (a *argMap) err() error {
	if len(a.problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid arguments: %s", strings.Join(a.problems, "; "))
}

func (a *argMap) str(key string) string {
	if v, ok := a.raw[key].(string); ok {
		return v
	}
	return ""
}

// has reports whether an argument was supplied at all. An empty string from
// `--arg key=` counts as absent, matching str and intOr.
func (a *argMap) has(key string) bool {
	v, ok := a.raw[key]
	if !ok || v == nil {
		return false
	}
	if s, isString := v.(string); isString && strings.TrimSpace(s) == "" {
		return false
	}
	return true
}

// required reads a string argument that must be present, recording a problem
// rather than returning early so every missing argument is reported at once.
func (a *argMap) required(key string) string {
	v := strings.TrimSpace(a.str(key))
	if v == "" {
		a.problems = append(a.problems, key+" is required")
	}
	return v
}

// requiredInt is required for a whole number. A missing replica count must not
// default to anything: zero would scale a workload to nothing.
func (a *argMap) requiredInt(key string) int {
	if !a.has(key) {
		a.problems = append(a.problems, key+" is required")
		return 0
	}
	return a.intOr(key, 0)
}

func (a *argMap) intOr(key string, fallback int) int {
	switch v := a.raw[key].(type) {
	case nil:
		return fallback
	case float64: // JSON numbers decode to float64
		if v != math.Trunc(v) {
			a.problems = append(a.problems, fmt.Sprintf("%s must be a whole number, got %v", key, v))
			return fallback
		}
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			a.problems = append(a.problems, fmt.Sprintf("%s must be a whole number, got %q", key, v))
			return fallback
		}
		return parsed
	default:
		a.problems = append(a.problems, fmt.Sprintf("%s must be a whole number", key))
		return fallback
	}
}

func (a *argMap) boolean(key string) bool {
	switch v := a.raw[key].(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		if strings.TrimSpace(v) == "" {
			return false
		}
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			a.problems = append(a.problems, fmt.Sprintf("%s must be true or false, got %q", key, v))
			return false
		}
		return parsed
	default:
		a.problems = append(a.problems, fmt.Sprintf("%s must be true or false", key))
		return false
	}
}

func marshalResult[T any](data T, err error) (subprocess.MCPCallResult, error) {
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal(data)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}
