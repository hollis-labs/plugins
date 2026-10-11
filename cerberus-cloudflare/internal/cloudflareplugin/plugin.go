package cloudflareplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sync"
	"syscall"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

// errMissingCredential is what every Cloudflare call returns when no token
// arrived. It is short on purpose: the host recognises a failure from a plugin
// that loaded without a required secret and appends its own guidance naming
// every way to supply one, so repeating it here would print it twice.
var errMissingCredential = errors.New("no Cloudflare API token was supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health,
// which the host passes through without adding its own. It is worded to
// survive the host's redact.Text: no "name: value" or "name=value" shapes, no
// flag followed by a word (see redaction_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Supply it as " + TokenEnvVar +
	", as the api_token entry under cloudflare in connector-secrets.yaml, or as keychain://cloudflare/api_token, " +
	"then reload the plugin with `cerberus connectors plugin managed load cloudflare`"

// Plugin serves the Cloudflare connector over the plugin-sdk subprocess
// protocol.
type Plugin struct {
	newBackend func(apiToken string) Backend
	config     subprocess.ConfigReader
	backend    Backend

	mu    sync.Mutex
	scrub scrubber
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against the live Cloudflare API. The token is not
// looked up here: Cerberus resolves every secret this manifest declares and
// hands the values over in init config.
func New() *Plugin { return &Plugin{newBackend: NewSDKBackend} }

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin {
	return &Plugin{
		newBackend: func(string) Backend { return backend },
		backend:    backend,
	}
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers the value
	// with the SDK logger's redaction tracker as well.
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus Cloudflare Connector",
		Version:     def.Version,
		Description: "Cloudflare zone and DNS record administration for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
		// Protocol 2 requires the result to echo the capability contract the
		// host offered; a host rejects the handshake as invalid_init without it.
		CapabilityContract: capability.ContractVersion,
	}, nil
}

// Load never fails on a missing token. The plugin starts, reports the gap in
// Health, and each Cloudflare call fails with errMissingCredential. Dry runs
// need no credential and keep working.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	token := p.token()
	p.mu.Lock()
	p.scrub = newScrubber(token)
	p.mu.Unlock()
	if p.backend == nil && token != "" {
		p.backend = p.newBackend(token)
	}
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.backend = nil
	return nil
}

// Health makes no network call: it reports whether the plugin can reach
// Cloudflare at all, which is whether a credential arrived.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	if p.backend == nil {
		return subprocess.HealthStatus{OK: false, Message: credentialGuidance}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "API token configured"}, nil
}

// token reads the credential the host resolved, falling back to the
// environment only for a binary run directly, outside the host, which receives
// no init config.
func (p *Plugin) token() string {
	if p.config != nil {
		if token := p.config.Secret(SecretAPIToken); token != "" {
			return token
		}
	}
	return os.Getenv(TokenEnvVar)
}

// writeOperations is the one list of operations that change Cloudflare. The
// contract declares each a non-read effect with a preview, and
// TestWriteOperationsAreExactlyTheAckGatedOnes holds the two in step.
var writeOperations = map[string]bool{
	"upsert_a_record":      true,
	"create_r2_credential": true,
	"revoke_r2_credential": true,
	"create_r2_bucket":     true,
	"delete_r2_bucket":     true,
	"create_zone":          true,
	"create_dns_record":    true,
	"delete_dns_record":    true,
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	result, err := p.call(ctx, req)
	if err == nil {
		return withOperationTelemetry(req, result), nil
	}
	scrub := p.scrubber()
	// The code is read before scrubbing, which drops the chain; the message
	// that carries it is scrubbed like any other.
	if code := errorCode(err); code != "" {
		return cerbplugin.ErrorResult(code, scrub.text(err.Error())), nil
	}
	return result, scrub.err(err)
}

func (p *Plugin) scrubber() scrubber {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scrub
}

// register adds a value minted during this process to what is scrubbed from
// every later error, so a credential that came back once cannot surface again
// in text composed from a later failure. Cerberus's own request scope cannot
// learn it: a plugin has no channel to hand the host a value at call time, which
// is why the result field is the only place it appears.
func (p *Plugin) register(secret string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scrub = p.scrub.with(secret)
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// optionalID reads an identifier that may be absent, refusing one that could
// not be a Cloudflare id (and so could not escape its path segment).
func optionalID(a *argMap, key string) string {
	v := a.str(key)
	if v != "" && !idPattern.MatchString(v) {
		a.problems = append(a.problems, key+" is not a valid Cloudflare id")
		return ""
	}
	return v
}

// requiredID is optionalID for an identifier the call cannot do without.
func requiredID(a *argMap, key string) string {
	v := a.required(key)
	if v != "" && !idPattern.MatchString(v) {
		a.problems = append(a.problems, key+" is not a valid Cloudflare id")
		return ""
	}
	return v
}

// errorCode is the Cerberus code for a failed call: a missing token is
// credential_missing, and a Cloudflare API that cannot be reached at all is
// unavailable. Anything the API answered stays uncoded.
func errorCode(err error) cerbplugin.ErrorCode {
	var coded *cerbplugin.CodedError
	var apiErr *apiError
	switch {
	case errors.As(err, &coded):
		return coded.Code
	case errors.Is(err, errMissingCredential):
		return cerbplugin.ErrorCredentialMissing
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized:
		return cerbplugin.ErrorCredentialMissing
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusBadRequest):
		return cerbplugin.ErrorInvalidArgs
	case isUnreachable(err):
		return cerbplugin.ErrorUnavailable
	}
	return ""
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
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
	args := newArgMap(req.Arguments)

	dryRun := false
	if writeOperations[op.Name] {
		dryRun = p.writeMode(args, op.Name)
	} else if args.boolean(argDryRun) {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
			fmt.Errorf("%s is read-only and has no dry-run preview; run it without --dry-run", op.Name))
	}

	switch op.Name {
	case "verify_token":
		accountID := optionalID(args, "account_id")
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.VerifyToken(ctx, accountID))

	case "list_zones":
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListZones(ctx))

	case "create_zone":
		accountID := args.required("account_id")
		name := args.required("name")
		zoneType := args.strOr("type", ZoneTypeFull)
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(newPreview(op.Name, "Would create a Cloudflare zone.",
				map[string]any{"account_id": accountID, "name": name},
				map[string]any{"type": zoneType}), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.CreateZone(ctx, accountID, name, zoneType))

	case "list_dns_records":
		zoneID := args.required("zone_id")
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListDNSRecords(ctx, zoneID))

	case "create_dns_record":
		zoneID := args.required("zone_id")
		rec := DNSRecord{
			Type:    args.required("type"),
			Name:    args.required("name"),
			Content: args.required("content"),
			// 1 is Cloudflare's "automatic" TTL, and the built-in's default.
			TTL:      args.intOr("ttl", 1),
			Proxied:  args.boolean("proxied"),
			Priority: args.intPtr("priority"),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(newPreview(op.Name, "Would create a Cloudflare DNS record.",
				map[string]any{"zone_id": zoneID, "name": rec.Name, "type": rec.Type},
				map[string]any{"content": rec.Content, "ttl": rec.TTL, "proxied": rec.Proxied, "priority": rec.Priority}), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.CreateDNSRecord(ctx, zoneID, rec))

	case "delete_dns_record":
		zoneID := args.required("zone_id")
		recordID := args.required("record_id")
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(newPreview(op.Name, "Would delete a Cloudflare DNS record.",
				map[string]any{"zone_id": zoneID, "record_id": recordID}, nil), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.backend.DeleteDNSRecord(ctx, zoneID, recordID); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(DeletedRecord{Deleted: true, ZoneID: zoneID, RecordID: recordID}, nil)

	case "list_r2_buckets":
		accountID := requiredID(args, "account_id")
		jurisdiction := args.oneOf("jurisdiction", jurisdictions)
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListBuckets(ctx, accountID, jurisdiction))

	case "create_r2_bucket":
		accountID := requiredID(args, "account_id")
		req := BucketRequest{
			Name:         bucketName(args, "name"),
			Jurisdiction: args.oneOf("jurisdiction", jurisdictions),
			LocationHint: args.oneOf("location_hint", locationHints),
			StorageClass: args.oneOf("storage_class", storageClasses),
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(p.createBucketPreview(ctx, accountID, req), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.CreateBucket(ctx, accountID, req))

	case "delete_r2_bucket":
		accountID := requiredID(args, "account_id")
		name := bucketName(args, "name")
		jurisdiction := args.oneOf("jurisdiction", jurisdictions)
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(p.deleteBucketPreview(ctx, accountID, name, jurisdiction), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.backend.DeleteBucket(ctx, accountID, name, jurisdiction); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(BucketDeleted{Deleted: true, AccountID: accountID, Name: name, Jurisdiction: jurisdiction}, nil)

	case "create_r2_credential":
		accountID := requiredID(args, "account_id")
		req := CredentialRequest{
			Bucket:       bucketName(args, "bucket"),
			Jurisdiction: args.oneOf("jurisdiction", jurisdictions),
			Permission:   args.oneOf("permission", permissions),
		}
		if req.Permission == "" {
			req.Permission = PermissionReadWrite
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(p.createCredentialPreview(ctx, accountID, req), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		// A typo in the bucket name must not mint a key for a bucket that
		// does not exist, or for a different one made later under that name.
		buckets, err := p.backend.ListBuckets(ctx, accountID, req.Jurisdiction)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if !hasBucket(buckets, req.Bucket) {
			return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
				fmt.Errorf("no R2 bucket named %s exists in this account and jurisdiction; no credential was minted", req.Bucket))
		}
		minted, err := p.backend.CreateR2Credential(ctx, accountID, req)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		// Registered before the result is built, so nothing composed after
		// this point can carry the value into an error.
		p.register(minted.AccessKeyValue)
		minted.Note = credentialNote
		return marshalResult(minted, nil)

	case "revoke_r2_credential":
		accountID := requiredID(args, "account_id")
		accessKeyID := requiredID(args, "access_key_id")
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(p.revokeCredentialPreview(ctx, accountID, accessKeyID), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		info, err := p.backend.GetR2Credential(ctx, accountID, accessKeyID)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if !mintedByThisPlugin(info) {
			return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
				fmt.Errorf("token %s was not minted by create_r2_credential, so revoke_r2_credential will not touch it; revoke it in the Cloudflare dashboard if that is intended", accessKeyID))
		}
		if err := p.backend.RevokeR2Credential(ctx, accountID, accessKeyID); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(CredentialRevoked{Revoked: true, AccountID: accountID, AccessKeyID: accessKeyID}, nil)

	case "upsert_a_record":
		zoneID := requiredID(args, "zone_id")
		name := args.required("name")
		if name != "" && !recordNamePattern.MatchString(name) {
			args.problems = append(args.problems, "name must be a host name such as app, app.example.com or @")
		}
		content := args.required("content")
		ttl := args.intPtr("ttl")
		if ttl != nil && *ttl != 1 && (*ttl < 60 || *ttl > 86400) {
			args.problems = append(args.problems, "ttl must be 1 (automatic) or between 60 and 86400 seconds")
		}
		if args.boolean("proxied") {
			args.problems = append(args.problems, "upsert_a_record is always DNS-only; proxied cannot be turned on")
		}
		if err := args.err(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := requireTailscaleAddress(content); err != nil {
			return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, err)
		}
		if dryRun {
			preview, err := p.upsertPreview(ctx, zoneID, name, content, ttl)
			return marshalResult(preview, err)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.upsert(ctx, zoneID, name, content, ttl))

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("operation %q is declared but not served", op.Name)
	}
}

// writeMode reads the dry-run flag for a write and refuses a real write that
// arrives unacknowledged.
//
// The host already refuses a write without --ack before the
// call reaches this process, so under the host this check never fires. It is
// here for the binary driven any other way, so that the plugin's own contract
// is "no unacknowledged write" rather than "no unacknowledged write provided
// the caller checked". A dry run changes nothing and needs no acknowledgment.
func (p *Plugin) writeMode(args *argMap, operation string) bool {
	dryRun := args.boolean(argDryRun)
	if !dryRun && !args.boolean(argAcknowledged) {
		args.problems = append(args.problems, operation+" changes Cloudflare and requires acknowledgment. Re-run it acknowledged (--ack), or preview it first with --dry-run")
	}
	return dryRun
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
