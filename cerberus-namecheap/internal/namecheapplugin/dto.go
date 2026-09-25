package namecheapplugin

import (
	"fmt"
	"sort"
	"strings"
)

// These are the only shapes this plugin returns. Each is an allow-list (ADR
// 0003 in the Cerberus repo), and each is built from Namecheap's XML in
// client.go, the one file that talks to the API. Field names and JSON tags
// match the compiled-in connector this plugin replaces, so output is
// unchanged for anyone reading it.

// Domain is the normalized view of a Namecheap domain.
type Domain struct {
	Name       string `json:"name"`
	Expires    string `json:"expires"`
	IsExpired  bool   `json:"is_expired"`
	IsLocked   bool   `json:"is_locked"`
	AutoRenew  bool   `json:"auto_renew"`
	WhoisGuard string `json:"whois_guard"`
}

// DNSRecord is a single DNS host record for a domain.
type DNSRecord struct {
	ID     int    `json:"id"`
	Type   string `json:"type"` // A, AAAA, CNAME, MX, TXT, NS
	Host   string `json:"host"`
	Value  string `json:"value"`
	TTL    int    `json:"ttl"`
	MXPref int    `json:"mx_pref,omitempty"`
}

// DomainStatus is the detailed status of a single domain.
type DomainStatus struct {
	Domain      string   `json:"domain"`
	Registered  bool     `json:"registered"`
	Expires     string   `json:"expires"`
	NameServers []string `json:"name_servers"`
}

// DomainNameserverUpdate reports the result of a nameserver change.
type DomainNameserverUpdate struct {
	Domain      string   `json:"domain"`
	Updated     bool     `json:"updated"`
	NameServers []string `json:"name_servers"`
}

// DNSRecordSet carries domain-level email routing alongside host records.
// Records returned by getHosts may be incomplete; they are not a zone backup.
type DNSRecordSet struct {
	EmailType string      `json:"email_type"`
	Records   []DNSRecord `json:"records"`
}

// emailTypes is every email routing mode setHosts accepts.
var emailTypes = []string{"MX", "MXE", "FWD", "OX", "NONE"}

// Validate refuses a set that would reset email routing to something the
// caller did not name, or that contradicts itself. It runs on the dry-run
// path and the real one alike.
func (set DNSRecordSet) Validate() error {
	known := false
	for _, t := range emailTypes {
		if set.EmailType == t {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("email_type must explicitly be MX, MXE, FWD, OX or NONE; refusing to reset an unknown email mode")
	}
	for _, record := range set.Records {
		if set.EmailType == "FWD" && (strings.EqualFold(record.Type, "MX") || strings.EqualFold(record.Type, "MXE")) {
			return fmt.Errorf("MX records conflict with email_type FWD; explicitly replace the complete record set with email_type MX to change email routing")
		}
	}
	return nil
}

// DryRunPreview is the preview a dry run returns. It has the same shape as the
// host's own previews (ExternalConnectorDryRunPreview in Cerberus), plus Diff
// for set_dns_record_set. The host cannot verify it: it is this plugin's claim
// of what the call would do.
type DryRunPreview struct {
	DryRun    bool           `json:"dry_run"`
	Connector string         `json:"connector"`
	Operation string         `json:"operation"`
	Summary   string         `json:"summary"`
	Target    map[string]any `json:"target,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
	Diff      *RecordSetDiff `json:"diff,omitempty"`
}

func newPreview(operation, summary string, target, input map[string]any, warnings ...string) DryRunPreview {
	return DryRunPreview{
		DryRun:    true,
		Connector: ConnectorID,
		Operation: operation,
		Summary:   summary,
		Target:    target,
		Input:     input,
		Warnings:  append([]string(nil), warnings...),
	}
}

// RecordSetDiff is what set_dns_record_set would change, computed against the
// records getHosts returns now. A record is matched on type, host, value, TTL
// and MX preference, so a TTL change appears as one removal and one addition.
// Remove lists what getHosts could see; a record it hides is deleted too and
// cannot appear here, which is why the warning stays.
type RecordSetDiff struct {
	Add       []DNSRecord  `json:"add"`
	Remove    []DNSRecord  `json:"remove"`
	Unchanged []DNSRecord  `json:"unchanged"`
	EmailType *EmailChange `json:"email_type,omitempty"`
}

// EmailChange is a change of email routing mode.
type EmailChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func recordKey(r DNSRecord) string {
	return fmt.Sprintf("%s|%s|%s|%d|%d", strings.ToUpper(r.Type), strings.ToLower(r.Host), r.Value, r.TTL, r.MXPref)
}

// diffRecordSets compares the proposed set with the current one. Duplicates
// are counted, so two identical proposed records against one current record
// are one unchanged and one added.
func diffRecordSets(current, proposed DNSRecordSet) RecordSetDiff {
	pool := map[string][]DNSRecord{}
	for _, r := range current.Records {
		pool[recordKey(r)] = append(pool[recordKey(r)], r)
	}
	diff := RecordSetDiff{Add: []DNSRecord{}, Remove: []DNSRecord{}, Unchanged: []DNSRecord{}}
	for _, r := range proposed.Records {
		key := recordKey(r)
		if matches := pool[key]; len(matches) > 0 {
			diff.Unchanged = append(diff.Unchanged, matches[0])
			pool[key] = matches[1:]
			continue
		}
		diff.Add = append(diff.Add, r)
	}
	keys := make([]string, 0, len(pool))
	for key := range pool {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		diff.Remove = append(diff.Remove, pool[key]...)
	}
	if !strings.EqualFold(current.EmailType, proposed.EmailType) {
		diff.EmailType = &EmailChange{From: current.EmailType, To: proposed.EmailType}
	}
	return diff
}
