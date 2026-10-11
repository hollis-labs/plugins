package cloudflareplugin

import (
	"context"
	"regexp"
)

// The values Cloudflare accepts for the R2 options this plugin passes through.
var (
	jurisdictions  = []string{"default", "eu", "fedramp"}
	locationHints  = []string{"apac", "eeur", "enam", "weur", "wnam", "oc"}
	storageClasses = []string{"Standard", "InfrequentAccess"}
)

// bucketNamePattern is R2's bucket naming rule: 3 to 63 characters, lowercase
// letters, digits and hyphens, beginning and ending with a letter or digit.
var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func bucketName(a *argMap, key string) string {
	v := a.required(key)
	if v != "" && !bucketNamePattern.MatchString(v) {
		a.problems = append(a.problems, key+" is not a valid R2 bucket name: use 3 to 63 lowercase letters, digits and hyphens, beginning and ending with a letter or digit")
		return ""
	}
	return v
}

// existingBuckets reads the account's buckets for a preview. A preview must
// still work without a credential, and a failed read must not stop one, so a
// gap comes back as a warning rather than an error.
func (p *Plugin) existingBuckets(ctx context.Context, accountID, jurisdiction string) ([]Bucket, string) {
	if p.backend == nil {
		return nil, "the account was not checked: no Cloudflare API token reached this plugin, so this preview is the request alone"
	}
	buckets, err := p.backend.ListBuckets(ctx, accountID, jurisdiction)
	if err != nil {
		return nil, "the account was not checked: " + p.scrubber().text(err.Error())
	}
	return buckets, ""
}

func (p *Plugin) createBucketPreview(ctx context.Context, accountID string, req BucketRequest) DryRunPreview {
	warnings := []string{"R2 storage and operations are billed by use; an empty bucket costs nothing"}
	buckets, gap := p.existingBuckets(ctx, accountID, req.Jurisdiction)
	if gap != "" {
		warnings = append(warnings, gap)
	}
	for _, b := range buckets {
		if b.Name == req.Name {
			warnings = append(warnings, "a bucket with this name already exists in the account, so the real call would be refused")
		}
	}
	return newPreview("create_r2_bucket", "Would create an R2 bucket. No bucket is created by this preview.",
		map[string]any{"account_id": accountID, "name": req.Name},
		map[string]any{"jurisdiction": orDefault(req.Jurisdiction), "location_hint": req.LocationHint, "storage_class": req.StorageClass},
		warnings...)
}

func (p *Plugin) deleteBucketPreview(ctx context.Context, accountID, name, jurisdiction string) DryRunPreview {
	warnings := []string{"Cloudflare refuses to delete a bucket that still holds objects. This plugin cannot list objects, so it cannot tell you beforehand whether this bucket is empty, and it will never empty one"}
	input := map[string]any{"jurisdiction": orDefault(jurisdiction)}
	buckets, gap := p.existingBuckets(ctx, accountID, jurisdiction)
	if gap != "" {
		warnings = append(warnings, gap)
	} else {
		found := false
		for _, b := range buckets {
			if b.Name == name {
				found = true
				input["bucket"] = b
			}
		}
		if !found {
			warnings = append(warnings, "no bucket with this name exists in the account")
		}
	}
	return newPreview("delete_r2_bucket", "Would delete an R2 bucket. Nothing is deleted by this preview.",
		map[string]any{"account_id": accountID, "name": name}, input, warnings...)
}

func orDefault(jurisdiction string) string {
	if jurisdiction == "" {
		return "default"
	}
	return jurisdiction
}
