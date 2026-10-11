package cloudflareplugin

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Permission values create_r2_credential accepts.
const (
	PermissionReadWrite = "read_write"
	PermissionRead      = "read"
)

var permissions = []string{PermissionReadWrite, PermissionRead}

// credentialNamePrefix starts the name of every API token this plugin mints
// for an R2 key. revoke_r2_credential acts only on a token carrying it, so a
// typo or a hostile id can never revoke an admin token or anyone else's key.
const credentialNamePrefix = "cerberus-r2:"

// r2PermissionGroup is the Cloudflare permission group, by name, that grants
// each level of object access to the buckets a policy names.
var r2PermissionGroup = map[string]string{
	PermissionReadWrite: "Workers R2 Storage Bucket Item Write",
	PermissionRead:      "Workers R2 Storage Bucket Item Read",
}

// credentialName is the token name for a key: the prefix, the bucket, the
// permission and the time, so an operator reading the dashboard can tell what
// each key is for.
func credentialName(req CredentialRequest, now time.Time) string {
	return fmt.Sprintf("%s%s:%s:%d", credentialNamePrefix, req.Bucket, req.Permission, now.Unix())
}

// r2Endpoint is the S3 endpoint for an account, and jurisdiction if it has one.
func r2Endpoint(accountID, jurisdiction string) string {
	if jurisdiction == "" || jurisdiction == "default" {
		return "https://" + accountID + ".r2.cloudflarestorage.com"
	}
	return "https://" + accountID + "." + jurisdiction + ".r2.cloudflarestorage.com"
}

const credentialNote = "access_key_value is shown this once and cannot be read again; store it now. Use access_key_id and access_key_value as the S3 key pair against the endpoint above. Revoke it with revoke_r2_credential"

func (p *Plugin) createCredentialPreview(ctx context.Context, accountID string, req CredentialRequest) DryRunPreview {
	warnings := []string{
		"this mints a credential. The secret half is returned once by the real call, is not stored or logged by this plugin, and cannot be read again",
		"the key reaches only this bucket and only object " + permissionWords(req.Permission) + "; it cannot list or change other buckets, or the account",
		"minting needs a token that can create account API tokens, which is more than R2 administration alone",
	}
	buckets, gap := p.existingBuckets(ctx, accountID, req.Jurisdiction)
	switch {
	case gap != "":
		warnings = append(warnings, gap)
	case !hasBucket(buckets, req.Bucket):
		warnings = append(warnings, "no bucket with this name exists in the account, so the real call would be refused")
	}
	return newPreview("create_r2_credential", "Would mint an S3 key pair scoped to one R2 bucket. No credential is created by this preview.",
		map[string]any{"account_id": accountID, "bucket": req.Bucket},
		map[string]any{
			"jurisdiction":     orDefault(req.Jurisdiction),
			"permission":       req.Permission,
			"permission_group": r2PermissionGroup[req.Permission],
			"token_name":       credentialNamePrefix + req.Bucket + ":" + req.Permission + ":<time>",
			"endpoint":         r2Endpoint(accountID, req.Jurisdiction),
		}, warnings...)
}

func permissionWords(permission string) string {
	if permission == PermissionRead {
		return "read"
	}
	return "read and write"
}

func hasBucket(buckets []Bucket, name string) bool {
	for _, b := range buckets {
		if b.Name == name {
			return true
		}
	}
	return false
}

// mintedByThisPlugin reports whether a token is one create_r2_credential made.
func mintedByThisPlugin(info *CredentialInfo) bool {
	return info != nil && strings.HasPrefix(info.Name, credentialNamePrefix)
}

func (p *Plugin) revokeCredentialPreview(ctx context.Context, accountID, accessKeyID string) DryRunPreview {
	var warnings []string
	input := map[string]any{}
	switch {
	case p.backend == nil:
		warnings = append(warnings, "the key was not checked: no Cloudflare API token reached this plugin, so this preview is the request alone")
	default:
		info, err := p.backend.GetR2Credential(ctx, accountID, accessKeyID)
		switch {
		case err != nil:
			warnings = append(warnings, "the key was not checked: "+p.scrubber().text(err.Error()))
		case !mintedByThisPlugin(info):
			warnings = append(warnings, "REFUSED: this token was not minted by create_r2_credential, and revoke_r2_credential acts only on keys that were. The real call would be refused")
			input["key"] = info
		default:
			input["key"] = info
		}
	}
	warnings = append(warnings, "the key stops working at once, and whatever uses it (a backup job, for one) starts failing")
	return newPreview("revoke_r2_credential", "Would revoke an R2 S3 key. Nothing is revoked by this preview.",
		map[string]any{"account_id": accountID, "access_key_id": accessKeyID}, input, warnings...)
}
