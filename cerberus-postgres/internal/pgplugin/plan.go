package pgplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
)

type Database struct {
	Name          string `json:"name" cerb:"untrusted"`
	Owner         string `json:"owner" cerb:"untrusted"`
	PublicConnect bool   `json:"public_connect"`
}
type Role struct {
	Name        string `json:"name" cerb:"untrusted"`
	Login       bool   `json:"login"`
	Superuser   bool   `json:"superuser"`
	CreateDB    bool   `json:"create_db"`
	CreateRole  bool   `json:"create_role"`
	Replication bool   `json:"replication"`
	BypassRLS   bool   `json:"bypass_rls"`
}
type Inventory struct {
	Cluster       string     `json:"cluster"`
	SystemID      string     `json:"system_id"`
	ServerVersion int        `json:"server_version"`
	Recovery      bool       `json:"recovery"`
	VectorVersion string     `json:"vector_available_version"`
	Databases     []Database `json:"databases"`
	Roles         []Role     `json:"roles"`
}
type AppRequest struct {
	Cluster     string `json:"cluster"`
	App         string `json:"app"`
	Environment string `json:"environment"`
	Vector      bool   `json:"vector"`
}
type Plan struct {
	Cluster        string   `json:"cluster"`
	App            string   `json:"app"`
	Environment    string   `json:"environment"`
	Database       string   `json:"database"`
	OwnerRole      string   `json:"owner_role"`
	MigrationLogin string   `json:"migration_login"`
	RuntimeRole    string   `json:"runtime_role"`
	RuntimeLogin   string   `json:"runtime_login"`
	Schema         string   `json:"schema"`
	Vector         bool     `json:"vector"`
	StateDigest    string   `json:"state_digest"`
	PlanDigest     string   `json:"plan_digest"`
	Executable     bool     `json:"executable"`
	Blockers       []string `json:"blockers" cerb:"untrusted"`
	Steps          []string `json:"steps"`
}

var slug = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func validate(r AppRequest) error {
	if !slug.MatchString(r.App) || len(r.App) > 20 || !slug.MatchString(r.Environment) || len(r.Environment) > 12 {
		return errors.New("app and environment must be bounded lowercase slugs without leading, trailing or repeated underscores")
	}
	return nil
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
func BuildPlan(r AppRequest, i Inventory) (Plan, error) {
	if err := validate(r); err != nil {
		return Plan{}, err
	}
	if r.Cluster == "" || r.Cluster != i.Cluster {
		return Plan{}, errors.New("cluster does not match configured target")
	}
	// A hashed JSON tuple avoids app/environment collisions (a_b,c vs a,b_c).
	// Digest suffix also leaves room for generation-specific login suffixes.
	suffix := digest([]string{r.Cluster, r.App, r.Environment})[7:23]
	prefix := "cb_" + r.App + "_" + suffix
	p := Plan{Cluster: r.Cluster, App: r.App, Environment: r.Environment, Database: prefix, OwnerRole: prefix + "_owner", MigrationLogin: prefix + "_m1", RuntimeRole: prefix + "_runtime", RuntimeLogin: prefix + "_r1", Schema: "app", Vector: r.Vector, Executable: false}
	// Sort copies: a caller's inventory is immutable and catalog row order is not state.
	i.Databases = append([]Database(nil), i.Databases...)
	i.Roles = append([]Role(nil), i.Roles...)
	sort.Slice(i.Databases, func(a, b int) bool { return i.Databases[a].Name < i.Databases[b].Name })
	sort.Slice(i.Roles, func(a, b int) bool { return i.Roles[a].Name < i.Roles[b].Name })
	p.StateDigest = digest(i)
	p.Blockers = []string{"host-owned durable credential issuance and apply path required; this release is preview-only"}
	if i.Recovery {
		p.Blockers = append(p.Blockers, "target is in recovery")
	}
	if i.ServerVersion < 170000 || i.ServerVersion >= 180000 {
		p.Blockers = append(p.Blockers, "first provisioning contract requires PostgreSQL 17")
	}
	if r.Vector && i.VectorVersion == "" {
		p.Blockers = append(p.Blockers, "pgvector is not available")
	}
	for _, db := range i.Databases {
		if db.Name == p.Database {
			p.Blockers = append(p.Blockers, "database name already exists; adoption requires a separate reviewed migration")
		}
		if db.PublicConnect {
			p.Blockers = append(p.Blockers, "existing database "+db.Name+" grants PUBLIC CONNECT; require reviewed HBA isolation or an explicit ACL migration before enabling a new tenant")
		}
	}
	for _, role := range i.Roles {
		if role.Name == p.OwnerRole || role.Name == p.MigrationLogin || role.Name == p.RuntimeRole || role.Name == p.RuntimeLogin {
			p.Blockers = append(p.Blockers, "planned role already exists: "+role.Name)
		}
	}
	p.Steps = []string{
		"Persist generation-specific migration and runtime credentials through the host-owned secret store before enabling either login; return references only.",
		"Create a non-login owner and non-login runtime privilege role; migration may SET ROLE owner; runtime cannot become owner or migration.",
		"Create a private app database, revoke PUBLIC CONNECT/TEMP, and grant CONNECT only to the app's migration/runtime roles.",
		"Revoke PUBLIC schema privileges; create schema app owned by the owner role; runtime receives USAGE only.",
		"Migrations SET ROLE owner; owner default table privileges grant SELECT/INSERT/UPDATE/DELETE and sequence USAGE/SELECT to runtime; revoke PUBLIC function EXECUTE defaults.",
		"Set a trusted search_path (pg_catalog, app); runtime has no CREATE, superuser, CREATEDB, CREATEROLE, REPLICATION or BYPASSRLS.",
		"Prove cross-database rejection and migration/runtime isolation with independent app identities before enabling the tenant.",
		"Require native TLS verify-full and mesh-only HBA/network reach before remote app adoption; no HTTP proxy.",
		"Rotation adds a new login generation; switch the app reference and prove reconnection before disabling/draining the old generation. Emergency revoke disables and terminates that login's sessions.",
	}
	if r.Vector {
		p.Steps = append(p.Steps, "Install only the reviewed pgvector extension version in the new app database; do not change existing tenants.")
	}
	p.PlanDigest = digest(p)
	return p, nil
}
