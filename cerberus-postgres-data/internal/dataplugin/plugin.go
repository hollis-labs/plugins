package dataplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerb "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
	"gopkg.in/yaml.v3"
)

const ID = "postgres-data"
const Binary = "cerberus-postgres-data"

func Definition() contract.Definition {
	fields := []contract.ConfigField{}
	for _, n := range []string{"cluster", "host", "port", "user", "database", "ca_file", "projections"} {
		fields = append(fields, contract.ConfigField{Name: n, Type: "string", Description: "Operator-owned data executor setting; projections is a JSON array of typed base-table definitions."})
	}
	return contract.Finalize(contract.Definition{ID: ID, Version: "0.1.0", ResourceTypes: []string{"server"}, Config: contract.ConfigSchema{Fields: fields, Secrets: []contract.SecretRequirement{{Name: "password", Required: true, Description: "Dedicated non-owner SELECT role; never an app or administrative login."}}}, Operations: []contract.Operation{{Name: "query", Description: "Execute one host-authorized projection; generic connector calls are refused.", Effect: contract.EffectReadSensitive, Target: contract.TargetDescriptor{Kind: "postgres.data", From: []string{"target"}}, Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone, InputSchema: contract.ObjectSchema(map[string]any{"target": contract.StringSchema("Published target"), "projection": contract.StringSchema("Published projection"), "version": contract.StringSchema("SHA256 of the exact published definition"), "parameters": map[string]any{"type": "object"}}, "target", "projection", "version", "parameters"), OutputSchema: contract.OutputSchemaOf[Result]()}}})
}
func Manifest() contract.Manifest { return contract.ManifestFromDefinition(Definition()) }
func WriteDist(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0755); err != nil {
		return err
	}
	spec := cerb.PluginYAMLFromManifest(Manifest(), cerb.Entrypoint{Command: "bin/" + Binary})
	spec.Cerberus.Host = cerb.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.Surfaces = cerb.Surfaces{MCP: []string{"query"}}
	spec.Capabilities = []subprocess.CapabilityRequest{{Name: cerb.DataQueryCapability}}
	b, err := yaml.Marshal(spec)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, cerb.PluginYAMLFilename), b, 0644)
}

type Plugin struct {
	mu          sync.Mutex
	cfg         Config
	projections []Projection
	permit      cerb.DataPermit
	used        bool
	ready       bool
}

func New() *Plugin { return &Plugin{} }
func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := subprocess.NewConfigReader(params.Config)
	p.cfg = Config{Cluster: r.String("cluster"), Host: r.String("host"), Port: r.String("port"), User: r.String("user"), Database: r.String("database"), CAFile: r.String("ca_file"), Password: r.Secret("password")}
	raw := r.String("projections")
	if raw == "" {
		raw = "[]"
	}
	if len(raw) > 65536 {
		return subprocess.InitResult{}, refused
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p.projections) != nil || d.Decode(new(any)) != io.EOF || len(p.projections) > 32 {
		return subprocess.InitResult{}, refused
	}
	seen := map[string]bool{}
	for _, v := range p.projections {
		if v.Validate() != nil || seen[v.Name] {
			return subprocess.InitResult{}, refused
		}
		seen[v.Name] = true
	}
	_ = json.Unmarshal([]byte(r.String(cerb.DataPermitConfigKey)), &p.permit)
	p.ready = len(p.projections) > 0
	p.used = false
	return subprocess.InitResult{ID: ID, Name: "PostgreSQL data projections", Version: "0.1.0", Protocol: subprocess.ProtocolVersion, CapabilityContract: capability.ContractVersion}, nil
}
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *Plugin) Unload(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Password = ""
	p.permit = cerb.DataPermit{}
	p.ready = false
	return nil
}
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := connectionConfig(p.cfg)
	return subprocess.HealthStatus{OK: p.ready && err == nil, Message: "Projection executor configured; data requests require a current host session grant."}, nil
}
func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	out, err := p.query(ctx, req)
	if err != nil {
		return cerb.ErrorResult(cerb.ErrorCode("data_refused"), refused.Error()), nil
	}
	b, err := json.Marshal(out)
	return subprocess.MCPCallResult{Content: b}, err
}
func (p *Plugin) query(ctx context.Context, req subprocess.MCPCallRequest) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	op, ok := cerb.OperationFromToolName(ID, req.ToolName, Manifest())
	if !ok || op.Name != "query" || !p.ready || p.used {
		return Result{}, refused
	}
	p.used = true
	b, err := json.Marshal(req.Arguments)
	if err != nil || len(b) > 65536 {
		return Result{}, refused
	}
	var q cerb.DataQuery
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF || q.Target != p.cfg.Cluster || p.permit.Validate(q, time.Now()) != nil {
		return Result{}, refused
	}
	for _, projection := range p.projections {
		if projection.Name == q.Projection && projection.Version() == q.Version {
			return execute(ctx, p.cfg, projection, q, p.permit)
		}
	}
	return Result{}, refused
}
