package digitaloceanplugin

import (
	"context"
	"fmt"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The intended posture, repeated in every operation that can widen a firewall.
const tailnetOnlyDescription = "Intended posture is tailnet-only (invariant H2, docs/plans/compute-hosting.md): the provider firewall denies all public inbound. A rule admitting 0.0.0.0/0 or ::/0 is allowed but the preview flags it loudly, and it should be deliberate."

var firewallRuleSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"protocol":           strProp("tcp, udp or icmp."),
		"ports":              strProp("A port, a range such as 8000-8100, or all. Omit for icmp."),
		"addresses":          listProp("IPs or CIDRs: sources for an inbound rule, destinations for an outbound one.", "string"),
		"tags":               listProp("Droplet tags.", "string"),
		"droplet_ids":        listProp("Droplet ids.", "integer"),
		"load_balancer_uids": listProp("Load balancer uids.", "string"),
		"kubernetes_ids":     listProp("Kubernetes cluster ids.", "string"),
	},
	"required": []string{"protocol"},
}

func rulesProp(desc string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": firewallRuleSchema}
}

const (
	firewallExample = "--arg firewall_id=00000000-0000-4000-8000-000000000000"
	createFWExample = `--arg name=tailnet-only --arg inbound_rules='[{"protocol":"udp","ports":"41641","addresses":["198.51.100.0/24"]}]'`
)

func registerFirewalls() {
	fwTarget := []string{"firewall_id"}
	registerFleet(readOp("list_firewalls", "digitalocean.account", nil,
		"List cloud firewalls, with a computed world_open flag on any that admit 0.0.0.0/0 or ::/0.",
		contract.OutputSchemaOf[FirewallList](), contract.ObjectSchema(map[string]any{}), "list_firewalls"),
		false, func(p *Plugin, ctx context.Context, _ *argMap, _ bool) (any, error) {
			if err := p.ready(); err != nil {
				return nil, err
			}
			list, err := p.backend.ListFirewalls(ctx)
			for i := range list.Firewalls {
				list.Firewalls[i].WorldOpen = hasWorldOpen(list.Firewalls[i].InboundRules)
			}
			return list, err
		})

	registerFleet(readOp("get_firewall", "digitalocean.firewall", fwTarget, "Get one cloud firewall with its rules and attachments.",
		contract.OutputSchemaOf[Firewall](), contract.ObjectSchema(map[string]any{"firewall_id": strProp("Firewall id.")}, "firewall_id"),
		"get_firewall "+firewallExample),
		false, func(p *Plugin, ctx context.Context, a *argMap, _ bool) (any, error) {
			id := a.id("firewall_id")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.firewall(p.backend.GetFirewall(ctx, id))
		})

	registerFleet(writeOp("create_firewall", contract.EffectWrite, contract.CostNone, "digitalocean.account", nil,
		"Create a cloud firewall. DigitalOcean denies all inbound not listed in inbound_rules, and all outbound not listed in outbound_rules. "+tailnetOnlyDescription,
		contract.OutputSchemaOf[Firewall](), contract.ObjectSchema(map[string]any{
			"name":           strProp("Firewall name."),
			"inbound_rules":  rulesProp("Inbound rules. Omit or leave empty to deny all inbound, which is the intended default."),
			"outbound_rules": rulesProp("Outbound rules. Empty means no egress at all."),
			"droplet_ids":    listProp("Droplets to attach.", "integer"),
			"tags":           listProp("Droplet tags to attach.", "string"),
		}, "name"), createFWExample),
		true, createFirewall)

	registerFleet(writeOp("update_firewall", contract.EffectWrite, contract.CostNone, "digitalocean.firewall", fwTarget,
		"Replace parts of a cloud firewall. Only the arguments given change: omitted name, rules, droplet_ids and tags are kept as they are. A given list replaces the existing one whole. "+tailnetOnlyDescription,
		contract.OutputSchemaOf[Firewall](), contract.ObjectSchema(map[string]any{
			"firewall_id":    strProp("Firewall id."),
			"name":           strProp("New name."),
			"inbound_rules":  rulesProp("Replacement inbound rules."),
			"outbound_rules": rulesProp("Replacement outbound rules."),
			"droplet_ids":    listProp("Replacement attached droplets.", "integer"),
			"tags":           listProp("Replacement attached tags.", "string"),
		}, "firewall_id"), firewallExample+` --arg inbound_rules='[]'`),
		true, updateFirewall)

	registerFleet(writeOp("delete_firewall", contract.EffectDestructive, contract.CostNone, "digitalocean.firewall", fwTarget,
		"Delete a cloud firewall. Every droplet it protected is left with whatever other firewalls it has, possibly none.",
		contract.OutputSchemaOf[ChangeResult](), contract.ObjectSchema(map[string]any{"firewall_id": strProp("Firewall id.")}, "firewall_id"),
		firewallExample),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			id := a.id("firewall_id")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("delete_firewall", "Would permanently delete cloud firewall "+id+". Droplets it protects lose that protection; if it was their only firewall they become fully exposed to the provider default.",
					map[string]any{"firewall_id": id}, nil).withWarnings(
					"Deleting a firewall removes its rules; droplets attached only to this firewall are left with no provider firewall."), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.DeleteFirewall(ctx, id); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "firewall", ID: id, Action: "delete"}, nil
		})

	for _, remove := range []bool{false, true} {
		name, verb := "assign_firewall_droplets", "attach"
		desc := "Attach droplets to a cloud firewall, so its rules apply to them."
		if remove {
			name, verb = "unassign_firewall_droplets", "detach"
			desc = "Detach droplets from a cloud firewall. They stop being protected by its rules."
		}
		effect := contract.EffectWrite
		if remove {
			effect = contract.EffectLifecycle
		}
		registerFleet(writeOp(name, effect, contract.CostNone, "digitalocean.firewall", fwTarget, desc,
			contract.OutputSchemaOf[ChangeResult](), contract.ObjectSchema(map[string]any{
				"firewall_id": strProp("Firewall id."),
				"droplet_ids": listProp("Droplet ids.", "integer"),
			}, "firewall_id", "droplet_ids"), firewallExample+" --arg droplet_ids=123456"),
			true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
				id := a.id("firewall_id")
				ids := a.intSlice("droplet_ids")
				if len(ids) == 0 {
					a.problems = append(a.problems, "droplet_ids is required and must not be empty")
				}
				if err := invalid(a.err()); err != nil {
					return nil, err
				}
				if dry {
					summary := fmt.Sprintf("Would %s droplet(s) %v to cloud firewall %s.", verb, ids, id)
					if remove {
						summary = fmt.Sprintf("Would detach droplet(s) %v from cloud firewall %s; they stop being protected by its rules.", ids, id)
					}
					return newPreview(name, summary, map[string]any{"firewall_id": id}, map[string]any{"droplet_ids": ids}), nil
				}
				if err := p.ready(); err != nil {
					return nil, err
				}
				var err error
				if remove {
					err = p.backend.RemoveFirewallDroplets(ctx, id, ids)
				} else {
					err = p.backend.AddFirewallDroplets(ctx, id, ids)
				}
				if err != nil {
					return nil, err
				}
				return ChangeResult{Resource: "firewall", ID: id, Action: verb, Detail: idsDetail(ids)}, nil
			})
	}
}

// firewall completes a firewall result with the computed world_open flag.
func (p *Plugin) firewall(fw *Firewall, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	fw.WorldOpen = hasWorldOpen(fw.InboundRules)
	return fw, nil
}

func createFirewall(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
	spec := FirewallSpec{Name: a.required("name"), DropletIDs: a.intSlice("droplet_ids"), Tags: a.strSlice("tags")}
	spec.InboundRules, _ = a.rules("inbound_rules")
	spec.OutboundRules, _ = a.rules("outbound_rules")
	if err := invalid(a.err()); err != nil {
		return nil, err
	}
	if dry {
		return firewallPreview("create_firewall", "Would create a DigitalOcean cloud firewall.", map[string]any{"name": spec.Name}, spec, true), nil
	}
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.firewall(p.backend.CreateFirewall(ctx, spec))
}

// updateFirewall reads the firewall first and overlays only what was given,
// because DigitalOcean's update is a full replace: sending just a new rule
// list would detach every droplet and tag.
func updateFirewall(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
	id := a.id("firewall_id")
	var name string
	if a.has("name") {
		name = a.required("name")
	}
	in, hasIn := a.rules("inbound_rules")
	out, hasOut := a.rules("outbound_rules")
	droplets, hasDroplets := a.intSlice("droplet_ids"), a.has("droplet_ids")
	tags, hasTags := a.strSlice("tags"), a.has("tags")
	if !(name != "" || hasIn || hasOut || hasDroplets || hasTags) {
		a.problems = append(a.problems, "give at least one of name, inbound_rules, outbound_rules, droplet_ids, tags to change")
	}
	if err := invalid(a.err()); err != nil {
		return nil, err
	}
	given := FirewallSpec{Name: name, InboundRules: in, OutboundRules: out, DropletIDs: droplets, Tags: tags}
	if dry {
		changing := map[string]any{"name": name != "", "inbound_rules": hasIn, "outbound_rules": hasOut, "droplet_ids": hasDroplets, "tags": hasTags}
		preview := firewallPreview("update_firewall",
			"Would update cloud firewall "+id+". Only the fields marked true under input.changing are replaced; the rest are kept.",
			map[string]any{"firewall_id": id}, given, hasIn)
		preview.Input["changing"] = changing
		return preview, nil
	}
	if err := p.ready(); err != nil {
		return nil, err
	}
	current, err := p.backend.GetFirewall(ctx, id)
	if err != nil {
		return nil, err
	}
	spec := FirewallSpec{Name: current.Name, InboundRules: current.InboundRules, OutboundRules: current.OutboundRules, DropletIDs: current.DropletIDs, Tags: current.Tags}
	if name != "" {
		spec.Name = name
	}
	if hasIn {
		spec.InboundRules = in
	}
	if hasOut {
		spec.OutboundRules = out
	}
	if hasDroplets {
		spec.DropletIDs = droplets
	}
	if hasTags {
		spec.Tags = tags
	}
	return p.firewall(p.backend.UpdateFirewall(ctx, id, spec))
}

// firewallPreview builds the preview for a create or update. When an inbound
// rule admits the whole internet the summary itself starts with the alarm,
// not only the warnings list, so a client that shows just the summary still
// shows it.
func firewallPreview(op, summary string, target map[string]any, spec FirewallSpec, checkInbound bool) DryRunPreview {
	input := map[string]any{
		"inbound_rules":  spec.InboundRules,
		"outbound_rules": spec.OutboundRules,
		"droplet_ids":    spec.DropletIDs,
		"tags":           spec.Tags,
	}
	if spec.Name != "" {
		input["name"] = spec.Name
	}
	preview := newPreview(op, summary, target, input)
	if !checkInbound {
		return preview
	}
	if findings := worldOpenFindings(spec.InboundRules); len(findings) > 0 {
		preview.Summary = "WORLD-OPEN FIREWALL: " + summary + " This opens the droplet(s) to the ENTIRE INTERNET."
		warnings := []string{"WORLD-OPEN: " + fmt.Sprint(len(findings)) + " inbound rule(s) admit 0.0.0.0/0 or ::/0."}
		for _, f := range findings {
			warnings = append(warnings, "WORLD-OPEN: "+f)
		}
		warnings = append(warnings, tailnetOnlyPosture)
		preview = preview.withWarnings(warnings...)
	}
	if op == "create_firewall" && len(spec.OutboundRules) == 0 {
		preview = preview.withWarnings("No outbound rules: DigitalOcean then blocks all egress from attached droplets, including DNS and the tailnet.")
	}
	return preview
}
