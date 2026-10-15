package digitaloceanplugin

import (
	"context"
	"fmt"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func registerAccount() {
	acct := "digitalocean.account"

	registerFleet(readOp("list_ssh_keys", acct, nil, "List the account's SSH keys by name and fingerprint.",
		contract.OutputSchemaOf[SSHKeyList](), contract.ObjectSchema(map[string]any{}), "list_ssh_keys"),
		false, func(p *Plugin, ctx context.Context, _ *argMap, _ bool) (any, error) {
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.ListSSHKeys(ctx)
		})

	registerFleet(writeOp("add_ssh_key", contract.EffectWrite, contract.CostNone, acct, nil,
		"Add a public SSH key to the account so new droplets can be created with it. Only the public half is accepted; a private key is refused.",
		contract.OutputSchemaOf[SSHKey](), contract.ObjectSchema(map[string]any{
			"name":       strProp("Key name."),
			"public_key": strProp("OpenSSH public key, for example the contents of id_ed25519.pub."),
		}, "name", "public_key"), "--arg name=laptop --arg public_key='ssh-ed25519 AAAA... me@example'"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			name := a.label("name", 255)
			key, keyType := a.publicKey("public_key")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("add_ssh_key", "Would add a "+keyType+" public SSH key to the DigitalOcean account.",
					map[string]any{"name": name}, map[string]any{"key_type": keyType, "public_key": userDataDigest(key)}), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.AddSSHKey(ctx, name, key)
		})

	registerFleet(writeOp("delete_ssh_key", contract.EffectDestructive, contract.CostNone, "digitalocean.ssh_key", []string{"key"},
		"Delete an SSH key from the account, by id or fingerprint. Droplets already created keep the key in their authorized_keys; it can no longer be chosen for new ones.",
		contract.OutputSchemaOf[ChangeResult](), contract.ObjectSchema(map[string]any{"key": strProp("Key id or fingerprint.")}, "key"),
		"--arg key=3b:16:bf:e4:8b:00:8b:b8:59:8c:a9:d3:f0:19:45:fa"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			key := a.id("key")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("delete_ssh_key",
					fmt.Sprintf("Would delete SSH key %s from the account. It cannot be selected for new droplets afterwards; droplets that already have it keep it in authorized_keys.", key),
					map[string]any{"key": key}, nil), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.DeleteSSHKey(ctx, key); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "ssh_key", ID: key, Action: "delete"}, nil
		})

	registerFleet(writeOp("assign_tag", contract.EffectWrite, contract.CostNone, "digitalocean.tag", []string{"tag"},
		"Apply a tag to droplets, volumes, volume snapshots or images, creating the tag if it does not exist. Firewalls that target the tag then apply to those droplets, so tagging can change what is exposed.",
		contract.OutputSchemaOf[ChangeResult](), contract.ObjectSchema(map[string]any{
			"tag":           strProp("Tag name."),
			"resource_type": strProp("droplet, volume, volume_snapshot or image."),
			"resource_ids":  listProp("Ids of the resources to tag.", "string"),
		}, "tag", "resource_type", "resource_ids"), "--arg tag=web --arg resource_type=droplet --arg resource_ids=123456"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			tag := a.tagName("tag")
			kind := a.oneOf("resource_type", "droplet", "volume", "volume_snapshot", "image")
			ids := a.requiredList("resource_ids")
			for _, id := range ids {
				if !idPattern.MatchString(id) {
					a.problems = append(a.problems, "resource_ids entries must be plain identifiers")
					break
				}
			}
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("assign_tag",
					fmt.Sprintf("Would tag %d %s resource(s) with %q, creating the tag if needed. Any firewall that targets this tag will start applying to droplets carrying it.", len(ids), kind, tag),
					map[string]any{"tag": tag}, map[string]any{"resource_type": kind, "resource_ids": ids}), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.TagResources(ctx, tag, kind, ids); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "tag", ID: tag, Action: "assign", Detail: ids}, nil
		})

	registerFleet(readOp("list_projects", acct, nil, "List projects, to find the id assign_project_resources needs.",
		contract.OutputSchemaOf[ProjectList](), contract.ObjectSchema(map[string]any{}), "list_projects"),
		false, func(p *Plugin, ctx context.Context, _ *argMap, _ bool) (any, error) {
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.ListProjects(ctx)
		})

	registerFleet(writeOp("assign_project_resources", contract.EffectWrite, contract.CostNone, "digitalocean.project", []string{"project_id"},
		"Move resources into a project by URN (for example do:droplet:123456 or do:volume:<id>). Organizes resources; it does not change their billing or networking.",
		contract.OutputSchemaOf[ChangeResult](), contract.ObjectSchema(map[string]any{
			"project_id": strProp("Project id."),
			"urns":       listProp("Resource URNs.", "string"),
		}, "project_id", "urns"), "--arg project_id=33333333-3333-4333-8333-333333333333 --arg urns=do:droplet:123456"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			id, urns := a.id("project_id"), a.urns("urns")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("assign_project_resources", fmt.Sprintf("Would move %d resource(s) into project %s.", len(urns), id),
					map[string]any{"project_id": id}, map[string]any{"urns": urns}), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.AssignProjectResources(ctx, id, urns); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "project", ID: id, Action: "assign", Detail: urns}, nil
		})

	registerFleet(readOp("list_reserved_ips", acct, nil, "List reserved IPs and the droplet each points at.",
		contract.OutputSchemaOf[ReservedIPList](), contract.ObjectSchema(map[string]any{}), "list_reserved_ips"),
		false, func(p *Plugin, ctx context.Context, _ *argMap, _ bool) (any, error) {
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.ListReservedIPs(ctx)
		})

	registerFleet(writeOp("assign_reserved_ip", contract.EffectLifecycle, contract.CostNone, "digitalocean.reserved_ip", []string{"ip"},
		"Point a reserved IP at a droplet. Traffic to the IP moves to that droplet immediately, away from whichever droplet had it.",
		contract.OutputSchemaOf[ChangeResult](), contract.ObjectSchema(map[string]any{
			"ip": strProp("The reserved IP address."), "droplet_id": intProp("Droplet id."),
		}, "ip", "droplet_id"), "--arg ip=203.0.113.10 --arg droplet_id=123456"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			ip, droplet := a.ipAddress("ip"), a.dropletID()
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("assign_reserved_ip",
					fmt.Sprintf("Would point reserved IP %s at droplet %d. Traffic to it moves there at once, away from any droplet that holds it now.", ip, droplet),
					map[string]any{"ip": ip, "droplet_id": droplet}, nil), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.AssignReservedIP(ctx, ip, droplet); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "reserved_ip", ID: ip, Action: "assign", DropletID: droplet}, nil
		})

	registerFleet(writeOp("unassign_reserved_ip", contract.EffectLifecycle, contract.CostNone, "digitalocean.reserved_ip", []string{"ip"},
		"Detach a reserved IP from its droplet. The IP stays reserved to the account, and an unassigned reserved IP can still be billed.",
		contract.OutputSchemaOf[ChangeResult](), contract.ObjectSchema(map[string]any{"ip": strProp("The reserved IP address.")}, "ip"),
		"--arg ip=203.0.113.10"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			ip := a.ipAddress("ip")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("unassign_reserved_ip",
					fmt.Sprintf("Would detach reserved IP %s from its droplet. Traffic to it stops being delivered, and the IP, still reserved, can keep billing while unassigned.", ip),
					map[string]any{"ip": ip}, nil), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.UnassignReservedIP(ctx, ip); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "reserved_ip", ID: ip, Action: "unassign"}, nil
		})

	registerFleet(readOp("list_vpcs", acct, nil, "List VPC networks.",
		contract.OutputSchemaOf[VPCList](), contract.ObjectSchema(map[string]any{}), "list_vpcs"),
		false, func(p *Plugin, ctx context.Context, _ *argMap, _ bool) (any, error) {
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.ListVPCs(ctx)
		})

	registerFleet(readOp("get_vpc", "digitalocean.vpc", []string{"vpc_id"}, "Get one VPC network.",
		contract.OutputSchemaOf[VPC](), contract.ObjectSchema(map[string]any{"vpc_id": strProp("VPC id.")}, "vpc_id"),
		"get_vpc --arg vpc_id=44444444-4444-4444-8444-444444444444"),
		false, func(p *Plugin, ctx context.Context, a *argMap, _ bool) (any, error) {
			id := a.id("vpc_id")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.GetVPC(ctx, id)
		})
}
