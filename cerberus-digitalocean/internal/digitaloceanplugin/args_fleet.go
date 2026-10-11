package digitaloceanplugin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// idPattern is the shape of every DigitalOcean identifier this plugin sends in
// a URL path: UUIDs, fingerprints (colon separated hex), numeric ids, slugs.
// godo joins the id into the request path as text, so an id carrying "/" or
// ".." would address a different endpoint; the pattern keeps that out.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// id reads a required identifier and refuses anything that is not path-safe.
func (a *argMap) id(key string) string {
	v := a.required(key)
	if v != "" && !idPattern.MatchString(v) {
		a.problems = append(a.problems, key+" is not a valid identifier")
		return ""
	}
	return v
}

// optID is id for an optional argument.
func (a *argMap) optID(key string) string {
	if a.str(key) == "" {
		return ""
	}
	return a.id(key)
}

func (a *argMap) has(key string) bool {
	v, ok := a.raw[key]
	return ok && v != nil
}

// positiveInt reads a required whole number of at least one.
func (a *argMap) positiveInt(key string) int {
	if a.str(key) == "" {
		a.problems = append(a.problems, key+" is required")
		return 0
	}
	n := a.intPtr(key)
	if n == nil {
		return 0
	}
	if *n < 1 {
		a.problems = append(a.problems, key+" must be a positive whole number")
		return 0
	}
	return *n
}

// intSlice reads a list of positive whole numbers: a JSON array, a lone
// number, or a comma separated string from the CLI.
func (a *argMap) intSlice(key string) []int {
	var items []any
	switch v := a.raw[key].(type) {
	case nil:
		return nil
	case []any:
		items = v
	case []int:
		return append([]int(nil), v...)
	case string:
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				items = append(items, part)
			}
		}
	default:
		items = []any{v}
	}
	var out []int
	for _, item := range items {
		n, ok := wholeNumber(item)
		if !ok || n < 1 {
			a.problems = append(a.problems, key+" entries must be positive whole numbers")
			return nil
		}
		out = append(out, n)
	}
	return out
}

func wholeNumber(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		return parsed, err == nil
	}
	return 0, false
}

// requiredList is strSlice that must come back non-empty.
func (a *argMap) requiredList(key string) []string {
	out := a.strSlice(key)
	if len(out) == 0 {
		a.problems = append(a.problems, key+" is required and must not be empty")
	}
	return out
}

// oneOf reads a required string that must be one of the allowed values.
func (a *argMap) oneOf(key string, allowed ...string) string {
	v := strings.ToLower(a.required(key))
	if v == "" {
		return ""
	}
	for _, ok := range allowed {
		if v == ok {
			return v
		}
	}
	a.problems = append(a.problems, fmt.Sprintf("%s must be one of %s", key, strings.Join(allowed, ", ")))
	return ""
}

// rules reads a firewall rule list. JSON callers send an array of objects; the
// CLI sends --arg inbound_rules='[...]' as one JSON string. present says the
// argument was given at all, which update_firewall needs to tell "leave the
// rules alone" from "clear them".
func (a *argMap) rules(key string) (rules []FirewallRule, present bool) {
	raw, ok := a.raw[key]
	if !ok || raw == nil {
		return nil, false
	}
	if s, isString := raw.(string); isString {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, false
		}
		var decoded any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			a.problems = append(a.problems, key+" must be a JSON array of rule objects")
			return nil, true
		}
		raw = decoded
	}
	list, isList := raw.([]any)
	if !isList {
		a.problems = append(a.problems, key+" must be an array of rule objects")
		return nil, true
	}
	out := []FirewallRule{}
	for i, item := range list {
		obj, isObj := item.(map[string]any)
		if !isObj {
			a.problems = append(a.problems, fmt.Sprintf("%s[%d] must be an object", key, i))
			continue
		}
		out = append(out, a.rule(fmt.Sprintf("%s[%d]", key, i), obj))
	}
	return out, true
}

// ruleKeys are the only keys a rule object may carry. An unknown key is an
// error rather than ignored: a misspelt "address" would otherwise become a
// rule with no source.
var ruleKeys = map[string]bool{
	"protocol": true, "ports": true, "addresses": true, "tags": true,
	"droplet_ids": true, "load_balancer_uids": true, "kubernetes_ids": true,
}

func (a *argMap) rule(where string, obj map[string]any) FirewallRule {
	for key := range obj {
		if !ruleKeys[key] {
			a.problems = append(a.problems, fmt.Sprintf("%s has an unknown field %q", where, key))
		}
	}
	sub := newArgMap(obj)
	rule := FirewallRule{
		Protocol:         strings.ToLower(sub.str("protocol")),
		Addresses:        sub.strSlice("addresses"),
		Tags:             sub.strSlice("tags"),
		DropletIDs:       sub.intSlice("droplet_ids"),
		LoadBalancerUIDs: sub.strSlice("load_balancer_uids"),
		KubernetesIDs:    sub.strSlice("kubernetes_ids"),
	}
	for _, problem := range sub.problems {
		a.problems = append(a.problems, where+": "+problem)
	}
	switch rule.Protocol {
	case "tcp", "udp", "icmp":
	default:
		a.problems = append(a.problems, where+": protocol must be tcp, udp or icmp")
	}
	rule.Ports = normalizePorts(sub.str("ports"))
	if rule.Protocol == "icmp" && rule.Ports != "" {
		a.problems = append(a.problems, where+": icmp takes no ports")
	}
	if rule.Protocol != "icmp" && !validPorts(rule.Ports) {
		a.problems = append(a.problems, where+": ports must be all, a port, or a range such as 8000-8100")
	}
	for _, addr := range rule.Addresses {
		if _, err := netip.ParsePrefix(addr); err != nil {
			if _, err := netip.ParseAddr(addr); err != nil {
				a.problems = append(a.problems, fmt.Sprintf("%s: %q is not an IP address or CIDR", where, addr))
			}
		}
	}
	if len(rule.Addresses)+len(rule.Tags)+len(rule.DropletIDs)+len(rule.LoadBalancerUIDs)+len(rule.KubernetesIDs) == 0 {
		a.problems = append(a.problems, where+": needs at least one of addresses, tags, droplet_ids, load_balancer_uids, kubernetes_ids")
	}
	return rule
}

func normalizePorts(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "all" {
		return "0"
	}
	return p
}

// validPorts accepts "0" (all ports), a port, or a low-high range.
func validPorts(p string) bool {
	if p == "" {
		return false
	}
	lo, hi, isRange := strings.Cut(p, "-")
	check := func(s string) bool {
		n, err := strconv.Atoi(s)
		return err == nil && n >= 0 && n <= 65535
	}
	if !isRange {
		return check(p)
	}
	return check(lo) && check(hi)
}
