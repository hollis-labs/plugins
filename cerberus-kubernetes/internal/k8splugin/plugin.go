package k8splugin

import (
	"context"
	"encoding/json"
	"fmt"
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
		Description: "Read-only Kubernetes cluster inspection for Cerberus",
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
			Namespace:     p.namespace(args),
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
			Namespace:     p.namespace(args),
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
			Namespace:     p.namespace(args),
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
			Namespace: p.namespace(args),
			Pod:       pod,
			Container: args.str("container"),
			Tail:      args.intOr("tail", DefaultLogTail),
			Previous:  args.boolean("previous"),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.Logs(ctx, opts, query))

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
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

func (p *Plugin) namespace(args *argMap) string {
	if ns := args.str("namespace"); ns != "" {
		return ns
	}
	if ns := p.configString(ConfigNamespace); ns != "" {
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

func (a *argMap) intOr(key string, fallback int) int {
	switch v := a.raw[key].(type) {
	case nil:
		return fallback
	case float64: // JSON numbers decode to float64
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
