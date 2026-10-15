package k8splugin

// Change is the result of every write operation, dry run or not.
//
// A write reports what it changed rather than returning the object it wrote:
// the object is a full upstream type, with every field dto.go exists to keep
// out. Changes is built from a fixed set of fields — replicas, a restart
// timestamp, schedulability — none of which can carry a credential.
//
// A dry run goes to the API server with dryRun=All. The server runs admission,
// validation and defaulting and then discards the result, so the preview is the
// cluster's own answer to "would this be accepted", not one composed here. That
// is a better preview than any this plugin could build, and it is why a
// dry-run write still needs a reachable cluster and the same RBAC as the real
// one.
type Change struct {
	Operation string `json:"operation"`

	// Target is "Kind/namespace/name", or "Node/name" for a cluster-scoped one.
	Target string `json:"target"`

	DryRun bool `json:"dry_run"`

	// Applied is true only when the change was really made. A dry run is never
	// applied, and neither is a no-op: cordoning a node that is already
	// cordoned sends nothing.
	Applied bool `json:"applied"`

	Changes []FieldChange `json:"changes,omitempty"`

	// Warnings name consequences an operator may not expect — deleting a pod
	// no controller owns does not bring it back.
	Warnings []string `json:"warnings,omitempty"`

	Message string `json:"message,omitempty"`
}

// FieldChange is one field's before and after.
type FieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}
