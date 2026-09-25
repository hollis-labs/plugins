package namecheapplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"syscall"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// errMissingCredential is what every Namecheap call returns when a required
// credential did not arrive. It is short on purpose: the host recognises a
// failure from a plugin that loaded without a required secret and appends its
// own guidance naming every way to supply one.
var errMissingCredential = errors.New("the Namecheap API user, key and username were not all supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health,
// which the host passes through without adding its own. It is worded to
// survive the host's redact.Text: no "name: value" or "name=value" shapes and
// no flag followed by a word (see redaction_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Supply each through " +
	strings.Join([]string{envVar(SecretAPIUser), envVar(SecretAPIKey), envVar(SecretUsername)}, ", ") +
	", as entries under namecheap in connector-secrets.yaml, or as keychain://namecheap/<name> references, " +
	"then reload the plugin with `cerberus connectors plugin managed load namecheap`"

// errPerRecordWrite is the refusal for the two per-record writes this plugin
// does not declare. The wording follows the compiled-in connector's, pointing
// at the command that replaced them.
var errPerRecordWrite = errors.New("namecheap per-record create and delete are disabled. getHosts can omit existing records and setHosts replaces the entire zone, " +
	"so a per-record write can silently delete what it could not see. Use `cerberus connectors exec namecheap set_dns_record_set` " +
	"with a complete authoritative record set and an explicit email_type, previewing it first with --dry-run")

// refusedTools are the tool names of the per-record writes the compiled-in
// connector once exposed. Neither is declared, so the host refuses them before
// the plugin is called; the plugin refuses them itself as well, before any
// credential is touched, for a caller speaking the protocol directly.
var refusedTools = []string{
	cerbplugin.ToolNameForOperation(ConnectorID, "create_dns_record"),
	cerbplugin.ToolNameForOperation(ConnectorID, "delete_dns_record"),
}

// setHostsWarning is the compiled-in connector's warning, kept verbatim: the
// diff can only show what getHosts returned.
const setHostsWarning = "All omitted records will be deleted. getHosts can omit existing records; supply a complete authoritative set."

const nameserverWarning = "Changing registrar nameservers moves DNS authority away from Namecheap's default nameservers for this domain."

// Plugin serves the Namecheap connector over the plugin-sdk subprocess
// protocol.
type Plugin struct {
	newBackend func(creds Credentials) Backend
	config     subprocess.ConfigReader
	backend    Backend
	scrub      scrubber
}

// Credentials are what the host resolves for this plugin.
type Credentials struct {
	APIUser  string
	APIKey   string
	Username string
	ClientIP string
	// Sandbox selects Namecheap's sandbox API. It travels with the
	// credentials because sandbox keys only work against the sandbox.
	Sandbox bool
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against the live Namecheap API. Credentials are not
// looked up here: Cerberus resolves every secret this manifest declares and
// hands the values over in init config.
func New() *Plugin {
	return &Plugin{newBackend: func(c Credentials) Backend {
		client := NewClient(c.APIUser, c.APIKey, c.Username, c.ClientIP)
		client.baseURL = BaseURL(c.Sandbox)
		return client
	}}
}

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin {
	return &Plugin{
		newBackend: func(Credentials) Backend { return backend },
		backend:    backend,
	}
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers each value
	// with the SDK logger's redaction tracker as well.
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus Namecheap Connector",
		Version:     def.Version,
		Description: "Namecheap domain and DNS administration for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load never fails on a missing credential. The plugin starts, reports the gap
// in Health, and each Namecheap call fails with errMissingCredential.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	creds := p.credentials()
	// The key always, and the other values when they are long enough to
	// remove without eating ordinary words, the same floor the host uses.
	scrubbed := []string{creds.APIKey}
	for _, value := range []string{creds.APIUser, creds.Username} {
		if len(value) >= 8 {
			scrubbed = append(scrubbed, value)
		}
	}
	p.scrub = newScrubber(scrubbed...)
	if p.backend == nil && creds.APIUser != "" && creds.APIKey != "" && creds.Username != "" {
		p.backend = p.newBackend(creds)
	}
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.backend = nil
	return nil
}

// Health makes no network call: it reports whether the credentials arrived.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	if p.backend == nil {
		return subprocess.HealthStatus{OK: false, Message: credentialGuidance}, nil
	}
	if p.sandbox() {
		return subprocess.HealthStatus{OK: true, Message: "API credentials configured, against the Namecheap sandbox"}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "API credentials configured, against production Namecheap"}, nil
}

// credentials reads what the host resolved, falling back to the environment
// only for a binary run directly, outside the host.
func (p *Plugin) credentials() Credentials {
	read := func(name string) string {
		if p.config != nil {
			if value := p.config.Secret(name); value != "" {
				return value
			}
		}
		return os.Getenv(envVar(name))
	}
	creds := Credentials{
		APIUser:  read(SecretAPIUser),
		APIKey:   read(SecretAPIKey),
		Username: read(SecretUsername),
		ClientIP: read(SecretClientIP),
	}
	if creds.ClientIP == "" {
		creds.ClientIP = DefaultClientIP
	}
	creds.Sandbox = p.sandbox()
	return creds
}

// sandbox reports whether to call the sandbox API: the config field when the
// host delivers one, or SandboxEnvVar for a binary run directly.
func (p *Plugin) sandbox() bool {
	if p.config != nil && p.config.Bool(ConfigSandbox) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(SandboxEnvVar))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// writeOperations are the operations that change Namecheap. Each needs
// acknowledgment and each has a dry-run preview; the contract says the same,
// and TestWriteOperationsAreExactlyTheAckGatedOnes holds the two in step.
var writeOperations = map[string]bool{
	"set_dns_record_set":     true,
	"set_custom_nameservers": true,
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	result, err := p.call(ctx, req)
	if err == nil {
		return withOperationTelemetry(req, result), nil
	}
	if isAddressRefused(err) {
		err = fmt.Errorf("%w. Namecheap checks the request address against the account's API allow-list; "+
			"the address sent is the client_ip secret (%s when unset), so set it to an allow-listed address and reload the plugin", err, DefaultClientIP)
	}
	// The code is read before scrubbing, which drops the chain; the message
	// that carries it is scrubbed like any other.
	if code := errorCode(err); code != "" {
		return cerbplugin.ErrorResult(code, p.scrub.text(err.Error())), nil
	}
	return result, p.scrub.err(err)
}

// credentialErrorNumbers are Namecheap API error numbers that mean the
// credentials, rather than the request, are the problem: a missing or invalid
// API user, key or username, API access not enabled, and a request address
// not on the account's allow-list.
var credentialErrorNumbers = []string{"1010101", "1010102", "1010104", "1011102", "1011104", "1011150"}

// errorCode is the Cerberus code for a failed call. Missing or rejected
// credentials, including an address Namecheap's allow-list refuses, are
// credential_missing; an API that cannot be reached at all is unavailable;
// arguments this plugin refused are invalid_args. Anything else the API
// answered stays uncoded.
func errorCode(err error) cerbplugin.ErrorCode {
	var coded *cerbplugin.CodedError
	var apiErr *APIError
	switch {
	case errors.As(err, &coded):
		return coded.Code
	case errors.Is(err, errMissingCredential):
		return cerbplugin.ErrorCredentialMissing
	case errors.As(err, &apiErr):
		for _, number := range apiErr.Numbers {
			if slices.Contains(credentialErrorNumbers, number) {
				return cerbplugin.ErrorCredentialMissing
			}
		}
		if isAddressRefused(err) || strings.Contains(strings.ToLower(apiErr.Message), "api key") {
			return cerbplugin.ErrorCredentialMissing
		}
		return ""
	case isUnreachable(err):
		return cerbplugin.ErrorUnavailable
	}
	return ""
}

// isAddressRefused matches Namecheap refusing the request address, whether by
// its error number or its wording.
func isAddressRefused(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if slices.Contains(apiErr.Numbers, "1011150") {
		return true
	}
	msg := strings.ToLower(apiErr.Message)
	return strings.Contains(msg, "request ip") || strings.Contains(msg, "whitelist")
}

// isUnreachable matches a network-level failure: a refused connection, a
// name that does not resolve, a timeout. An HTTP status is not one of these.
func isUnreachable(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

func (p *Plugin) call(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	if slices.Contains(refusedTools, req.ToolName) {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, errPerRecordWrite)
	}
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
	args := newArgMap(req.Arguments)

	dryRun := false
	if writeOperations[op.Name] {
		var err error
		if dryRun, err = p.writeMode(args, op.Name); err != nil {
			return subprocess.MCPCallResult{}, err
		}
	} else if args.boolean(argDryRun) {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
			fmt.Errorf("%s is read-only and has no dry-run preview; run it without --dry-run", op.Name))
	}

	switch op.Name {
	case "list_domains":
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListDomains(ctx))

	case "get_domain_status":
		domain := args.required("domain")
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.GetDomainStatus(ctx, domain))

	case "get_dns_record_set", "list_dns_records":
		sld, tld, err := domainArg(args)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		set, err := p.backend.GetDNSRecordSet(ctx, sld, tld)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if op.Name == "list_dns_records" {
			return marshalResult(set.Records, nil)
		}
		return marshalResult(set, nil)

	case "set_dns_record_set":
		domain := args.required("domain")
		set := DNSRecordSet{EmailType: args.required("email_type"), Records: args.records()}
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := invalid(set.Validate()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		sld, tld, err := SplitDomain(domain)
		if err != nil {
			return subprocess.MCPCallResult{}, invalid(err)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			// The preview reads the zone as it is now. A failed read fails
			// the dry run: a preview that silently fell back to no diff
			// would claim nothing is removed.
			current, err := p.backend.GetDNSRecordSet(ctx, sld, tld)
			if err != nil {
				return subprocess.MCPCallResult{}, err
			}
			preview := newPreview(op.Name, "Would replace every Namecheap DNS host record and explicitly set email routing.",
				map[string]any{"domain": domain}, map[string]any{"email_type": set.EmailType, "records": set.Records}, setHostsWarning)
			diff := diffRecordSets(*current, set)
			preview.Diff = &diff
			return marshalResult(preview, nil)
		}
		if err := p.backend.SetDNSRecordSet(ctx, sld, tld, set); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(set, nil)

	case "set_custom_nameservers":
		domain := args.required("domain")
		var nameservers []string
		for _, ns := range args.strSlice("nameservers") {
			if ns = strings.TrimSpace(ns); ns != "" {
				nameservers = append(nameservers, ns)
			}
		}
		if len(nameservers) < 2 {
			args.problems = append(args.problems, "nameservers must list at least two nameservers")
		}
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if _, _, err := SplitDomain(domain); err != nil {
			return subprocess.MCPCallResult{}, invalid(err)
		}
		if dryRun {
			return marshalResult(newPreview(op.Name, "Would switch a Namecheap domain to custom nameservers.",
				map[string]any{"domain": domain}, map[string]any{"nameservers": nameservers}, nameserverWarning), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.SetCustomNameservers(ctx, domain, nameservers))

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("operation %q is declared but not served", op.Name)
	}
}

// domainArg reads a required domain and splits it the way Namecheap's DNS
// commands want it.
func domainArg(args *argMap) (string, string, error) {
	domain := args.required("domain")
	if err := invalid(args.err()); err != nil {
		return "", "", err
	}
	sld, tld, err := SplitDomain(domain)
	if err != nil {
		return "", "", invalid(err)
	}
	return sld, tld, nil
}

// writeMode reads the dry-run flag for a write and refuses a real write that
// arrives unacknowledged.
//
// The host already refuses an acknowledgment-gated operation without --ack
// before the call reaches this process, so under the host this check never
// fires. It is here for the binary driven any other way, so that the plugin's
// own contract is "no unacknowledged write" rather than "no unacknowledged
// write provided the caller checked". A dry run changes nothing and needs no
// acknowledgment.
func (p *Plugin) writeMode(args *argMap, operation string) (bool, error) {
	dryRun := args.boolean(argDryRun)
	if !dryRun && !args.boolean(argAcknowledged) {
		return false, fmt.Errorf("%s changes Namecheap and requires acknowledgment. Re-run it acknowledged (--ack), or preview it first with --dry-run", operation)
	}
	return dryRun, nil
}

// invalid codes this plugin's own refusal of the caller's arguments.
func invalid(err error) error {
	if err == nil {
		return nil
	}
	return cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, err)
}

func (p *Plugin) ready() error {
	if p.backend == nil {
		return errMissingCredential
	}
	return nil
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
