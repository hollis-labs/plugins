package pgplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

type Plugin struct {
	config   Config
	backend  Backend
	issuance cerbplugin.Issuance
	issued   map[string]string
}

func New() *Plugin { return &Plugin{} }
func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	r := subprocess.NewConfigReader(params.Config)
	p.config = Config{Cluster: r.String("cluster"), Host: r.String("host"), Port: r.String("port"), User: r.String("user"), Database: r.String("database"), CAFile: r.String("ca_file"), Password: r.Secret("password"), AdminUser: r.String("admin_user"), AdminPassword: r.Secret("admin_password")}
	p.issuance, _ = cerbplugin.IssuanceFromConfig(params.Config)
	p.issued = map[string]string{}
	for _, slot := range []string{"password", "migration_password", "runtime_password"} {
		p.issued[slot] = r.Secret(cerbplugin.IssuedValuePrefix + slot)
	}
	p.backend = &postgresBackend{config: p.config}
	return subprocess.InitResult{ID: ConnectorID, Name: "PostgreSQL administration", Version: Version, Protocol: subprocess.ProtocolVersion, CapabilityContract: capability.ContractVersion}, nil
}
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *Plugin) Unload(context.Context) error {
	p.backend = nil
	p.config.Password = ""
	p.config.AdminPassword = ""
	p.issued = nil
	p.issuance = cerbplugin.Issuance{}
	return nil
}
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	_, err := connectionConfig(p.config)
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "Configure the named PostgreSQL target and its declared password; TCP requires TLS verify-full."}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "Target configured; use inspect to check database connectivity."}, nil
}
func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	value, err := p.call(ctx, req)
	if err != nil {
		return cerbplugin.ErrorResult(cerbplugin.ErrorCode("postgres_refused"), err.Error()), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return subprocess.MCPCallResult{}, errors.New("PostgreSQL result encoding failed")
	}
	return subprocess.MCPCallResult{Content: data}, nil
}
func (p *Plugin) call(ctx context.Context, req subprocess.MCPCallRequest) (any, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return nil, errors.New("unknown PostgreSQL operation")
	}
	if op.Name == "provision_app" || op.Name == "rotate_login" || op.Name == "retire_login" || op.Name == "revoke_login" || op.Name == "adopt_app" || op.Name == "retire_legacy" {
		return p.lifecycle(ctx, op.Name, req.Arguments)
	}
	if p.config.AdminPassword != "" {
		return nil, errors.New("read process must not hold admin_password; configure explicit read/write credential bindings")
	}
	// Reject hidden password, DSN, SQL and endpoint inputs even if a caller
	// bypasses schema validation. Both existing operations are read-only.
	allowed := map[string]bool{"cluster": true}
	if op.Name == "plan_app" {
		allowed["app"] = true
		allowed["environment"] = true
		allowed["vector"] = true
	}
	for k := range req.Arguments {
		if !allowed[k] {
			return nil, errors.New("unexpected PostgreSQL operation argument")
		}
	}
	raw, err := json.Marshal(req.Arguments)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL operation arguments")
	}
	var r AppRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return nil, errors.New("invalid PostgreSQL operation argument type")
	}
	if r.Cluster == "" || r.Cluster != p.config.Cluster {
		return nil, errors.New("cluster does not match the operator-configured target")
	}
	if op.Name == "plan_app" {
		if err = validate(r); err != nil {
			return nil, err
		}
	}
	if p.backend == nil {
		return nil, errors.New("PostgreSQL plugin is not loaded")
	}
	i, err := p.backend.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	if op.Name == "inspect" {
		return i, nil
	}
	return BuildPlan(r, i)
}

func (p *Plugin) lifecycle(ctx context.Context, op string, args map[string]any) (any, error) {
	definition, _ := Definition().Operation(op)
	allowed := map[string]bool{"dry_run": true, "acknowledged": true}
	for _, input := range definition.Inputs {
		allowed[input.Name] = true
	}
	for key := range args {
		if !allowed[key] {
			return nil, errors.New("unexpected PostgreSQL operation argument")
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, errors.New("invalid lifecycle arguments")
	}
	var r LifecycleRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return nil, errors.New("invalid lifecycle argument type")
	}
	if r.Cluster == "" || r.Cluster != p.config.Cluster {
		return nil, errors.New("cluster does not match the operator-configured target")
	}
	if err = validateLifecycle(op, r); err != nil {
		return nil, err
	}
	b, ok := p.backend.(LifecycleBackend)
	if !ok {
		return nil, errors.New("PostgreSQL lifecycle backend unavailable")
	}
	if r.DryRun {
		if p.config.AdminPassword != "" {
			return nil, errors.New("preview process must not hold admin_password; configure explicit read/write credential bindings")
		}
		return b.Preview(ctx, op, r)
	}
	return b.Execute(ctx, op, r, p.issuance, p.issued)
}
