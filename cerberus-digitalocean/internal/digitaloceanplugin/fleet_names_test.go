package digitaloceanplugin

// The fleet operation set, in registration order, with each one's effect.
// Adding an operation means adding it here: the contract tests then hold the
// gates to it.
var fleetOperationNames = []string{
	"list_firewalls", "get_firewall", "create_firewall", "update_firewall", "delete_firewall",
	"assign_firewall_droplets", "unassign_firewall_droplets",
	"list_volumes", "create_volume", "attach_volume", "detach_volume", "resize_volume", "delete_volume",
	"list_snapshots", "create_volume_snapshot", "create_droplet_snapshot", "delete_snapshot",
}

var fleetEffects = map[string]string{
	"list_firewalls": "read", "get_firewall": "read",
	"create_firewall": "write", "update_firewall": "write", "delete_firewall": "destructive",
	"assign_firewall_droplets": "write", "unassign_firewall_droplets": "lifecycle",
	"list_volumes": "read", "create_volume": "write", "attach_volume": "write", "detach_volume": "lifecycle",
	"resize_volume": "write", "delete_volume": "destructive",
	"list_snapshots": "read", "create_volume_snapshot": "write", "create_droplet_snapshot": "write", "delete_snapshot": "destructive",
}

// destructiveOperations is every operation whose contract is Destructive.
var destructiveOperations = map[string]bool{
	"destroy": true, "delete_firewall": true, "delete_volume": true, "delete_snapshot": true,
}
