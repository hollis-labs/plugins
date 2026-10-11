package digitaloceanplugin

// The fleet operation set, in registration order, with each one's effect.
// Adding an operation means adding it here: the contract tests then hold the
// gates to it.
var fleetOperationNames = []string{
	"list_firewalls", "get_firewall", "create_firewall", "update_firewall", "delete_firewall",
	"assign_firewall_droplets", "unassign_firewall_droplets",
}

var fleetEffects = map[string]string{
	"list_firewalls": "read", "get_firewall": "read",
	"create_firewall": "write", "update_firewall": "write", "delete_firewall": "destructive",
	"assign_firewall_droplets": "write", "unassign_firewall_droplets": "lifecycle",
}

// destructiveOperations is every operation whose contract is Destructive.
var destructiveOperations = map[string]bool{
	"destroy": true, "delete_firewall": true,
}
