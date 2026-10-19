package pgplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/jackc/pgx/v5"
)

// No endpoint, SQL, password or credential-reference input is accepted. The
// host supplies newly persisted values privately, after its policy gates.
type LifecycleRequest struct {
	AppRequest
	IssuanceID   string `json:"issuance_id,omitempty"`
	ExpectedPlan string `json:"expected_plan,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Generation   int    `json:"generation,omitempty"`
	Consumer     string `json:"consumer,omitempty"`
	DryRun       bool   `json:"dry_run,omitempty"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
}

type Login struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Generation int    `json:"generation"`
	Enabled    bool   `json:"enabled"`
}

type LifecyclePlan struct {
	Operation   string   `json:"operation"`
	App         Plan     `json:"app"`
	Login       string   `json:"login,omitempty"`
	Replacement string   `json:"replacement,omitempty"`
	Logins      []Login  `json:"logins"`
	Witness     string   `json:"consumer_witness,omitempty"`
	PlanDigest  string   `json:"plan_digest"`
	Executable  bool     `json:"executable"`
	Blockers    []string `json:"blockers" cerb:"untrusted"`
	Steps       []string `json:"steps"`
}

type LifecycleResult struct {
	Operation         string `json:"operation"`
	Database          string `json:"database"`
	Schema            string `json:"schema"`
	OwnerRole         string `json:"owner_role"`
	MigrationLogin    string `json:"migration_login,omitempty"`
	RuntimeLogin      string `json:"runtime_login,omitempty"`
	Login             string `json:"login,omitempty"`
	Disabled          bool   `json:"disabled,omitempty"`
	RemainingSessions int    `json:"remaining_sessions"`
	Next              string `json:"next"`
}

type LifecycleBackend interface {
	Preview(context.Context, string, LifecycleRequest) (LifecyclePlan, error)
	Execute(context.Context, string, LifecycleRequest, cerbplugin.Issuance, map[string]string) (LifecycleResult, error)
}

// Comments identify objects this plugin created; they are provenance, not an
// authorization boundary against a PostgreSQL superuser. Never adopt a name.
type ownership struct {
	Version     int    `json:"version"`
	Cluster     string `json:"cluster"`
	SystemID    string `json:"system_id"`
	App         string `json:"app"`
	Environment string `json:"environment"`
	Kind        string `json:"kind"`
	Generation  int    `json:"generation"`
	IssuanceID  string `json:"issuance_id"`
	CreatedAt   string `json:"created_at"`
	Ready       bool   `json:"ready"`
}

type managedRole struct {
	Role    Role
	Comment string
}
type membership struct {
	Role, Member        string
	Admin, Inherit, Set bool
}
type appState struct {
	Inventory       Inventory
	DatabaseComment string
	Roles           []managedRole
	Memberships     []membership
}

var issuanceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var planPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateLifecycle(op string, r LifecycleRequest) error {
	if op != "provision_app" && op != "rotate_login" && op != "retire_login" && op != "revoke_login" {
		return errors.New("unknown lifecycle operation")
	}
	if err := validate(r.AppRequest); err != nil {
		return err
	}
	if op == "provision_app" || op == "rotate_login" {
		if !issuanceIDPattern.MatchString(r.IssuanceID) {
			return errors.New("issuance_id must be a fresh 32-character lowercase hex identifier")
		}
	}
	if op != "provision_app" {
		if r.Kind != "runtime" && r.Kind != "migration" {
			return errors.New("kind must be runtime or migration")
		}
	}
	if op == "retire_login" || op == "revoke_login" {
		if r.Generation < 1 || r.Generation > 999999 {
			return errors.New("generation must be 1..999999")
		}
	}
	if op == "retire_login" && (!slug.MatchString(r.Consumer) || len(r.Consumer) > 48) {
		return errors.New("consumer must identify the replacement connection's application_name (a lowercase slug, max 48 characters)")
	}
	if !r.DryRun && (!r.Acknowledged || !planPattern.MatchString(r.ExpectedPlan)) {
		return errors.New("write requires acknowledgment and the expected_plan digest from a fresh dry-run")
	}
	return nil
}

func loginName(p Plan, kind string, generation int) string {
	suffix := "r"
	if kind == "migration" {
		suffix = "m"
	}
	return p.Database + "_" + suffix + strconv.Itoa(generation)
}

func marker(r LifecycleRequest, i Inventory, kind string, generation int) ownership {
	return ownership{Version: 1, Cluster: r.Cluster, SystemID: i.SystemID, App: r.App, Environment: r.Environment, Kind: kind, Generation: generation, IssuanceID: r.IssuanceID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}
func owned(raw string, r LifecycleRequest, i Inventory, kind string) (ownership, bool) {
	var m ownership
	err := json.Unmarshal([]byte(raw), &m)
	return m, err == nil && m.Version == 1 && m.Cluster == r.Cluster && m.SystemID == i.SystemID && m.App == r.App && m.Environment == r.Environment && m.Kind == kind && issuanceIDPattern.MatchString(m.IssuanceID)
}

func (b *postgresBackend) connect(ctx context.Context, write bool, database string) (*pgx.Conn, error) {
	c := b.config
	if write {
		if c.AdminUser == "" || c.AdminPassword == "" {
			return nil, errors.New("write-bound admin_user and admin_password are required; no reader-credential fallback")
		}
		c.User, c.Password = c.AdminUser, c.AdminPassword
	}
	if database != "" {
		c.Database = database
	}
	cfg, err := connectionConfig(c)
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["application_name"] = "cerberus-postgres"
	cfg.RuntimeParams["statement_timeout"] = "10000"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("PostgreSQL target connection failed")
	}
	return conn, nil
}

func closeConn(c *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.Close(ctx)
}

func snapshot(ctx context.Context, c *pgx.Conn, r LifecycleRequest) (appState, error) {
	i, err := inspectConn(ctx, c, r.Cluster)
	if err != nil {
		return appState{}, err
	}
	p, err := BuildPlan(r.AppRequest, i)
	if err != nil {
		return appState{}, err
	}
	s := appState{Inventory: i, Roles: []managedRole{}, Memberships: []membership{}}
	err = c.QueryRow(ctx, `SELECT coalesce((SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=$1),'')`, p.Database).Scan(&s.DatabaseComment)
	if err != nil {
		return s, errors.New("database provenance read failed")
	}
	rows, err := c.Query(ctx, `SELECT r.rolname,coalesce(shobj_description(r.oid,'pg_authid'),'') FROM pg_roles r WHERE starts_with(r.rolname,$1) ORDER BY r.rolname LIMIT 1025`, p.Database+"_")
	if err != nil {
		return s, errors.New("role provenance read failed")
	}
	comments := map[string]string{}
	for rows.Next() {
		var name, comment string
		if err = rows.Scan(&name, &comment); err != nil {
			break
		}
		comments[name] = comment
	}
	rows.Close()
	if err != nil || rows.Err() != nil || len(comments) > 1024 {
		return s, errors.New("role provenance inventory incomplete")
	}
	for _, role := range i.Roles {
		if comment, ok := comments[role.Name]; ok {
			s.Roles = append(s.Roles, managedRole{role, comment})
		}
	}
	rows, err = c.Query(ctx, `SELECT parent.rolname,child.rolname,m.admin_option,m.inherit_option,m.set_option FROM pg_auth_members m JOIN pg_roles parent ON parent.oid=m.roleid JOIN pg_roles child ON child.oid=m.member WHERE starts_with(parent.rolname,$1) OR starts_with(child.rolname,$1) ORDER BY parent.rolname,child.rolname LIMIT 2049`, p.Database+"_")
	if err != nil {
		return s, errors.New("role membership read failed")
	}
	for rows.Next() {
		var m membership
		if err = rows.Scan(&m.Role, &m.Member, &m.Admin, &m.Inherit, &m.Set); err != nil {
			break
		}
		s.Memberships = append(s.Memberships, m)
	}
	rows.Close()
	if err != nil || rows.Err() != nil || len(s.Memberships) > 2048 {
		return s, errors.New("role membership inventory incomplete")
	}
	return s, nil
}

func lifecyclePlan(ctx context.Context, c *pgx.Conn, op string, r LifecycleRequest, s appState) (LifecyclePlan, error) {
	p, err := BuildPlan(r.AppRequest, s.Inventory)
	if err != nil {
		return LifecyclePlan{}, err
	}
	l := LifecyclePlan{Operation: op, App: p, Logins: []Login{}, Blockers: []string{}, Steps: []string{}}
	if op == "provision_app" {
		l.Blockers = append(l.Blockers, p.Blockers...)
		l.Steps = p.Steps
	} else {
		l.App.Blockers = nil
		l.App.Executable = false
		l.App.PlanDigest = ""
		m, ok := owned(s.DatabaseComment, r, s.Inventory, "database")
		if !ok || !m.Ready {
			l.Blockers = append(l.Blockers, "database is not a completed plugin-managed app; inspect and reconcile partial or foreign state explicitly")
		}
		for _, db := range s.Inventory.Databases {
			if db.Name == p.Database && db.Owner != p.OwnerRole {
				l.Blockers = append(l.Blockers, "managed database ownership has drifted")
			}
		}
		if s.Inventory.Recovery || s.Inventory.ServerVersion/10000 != 17 {
			l.Blockers = append(l.Blockers, "lifecycle writes require a writable PostgreSQL 17 primary")
		}
		maxGeneration := 0
		roles := map[string]ownership{}
		for _, mr := range s.Roles {
			var rm ownership
			_ = json.Unmarshal([]byte(mr.Comment), &rm)
			rm, valid := owned(mr.Comment, r, s.Inventory, rm.Kind)
			valid = valid && (rm.Kind == "owner" || rm.Kind == "runtime_role" || rm.Kind == "runtime" || rm.Kind == "migration")
			if rm.Kind == "owner" || rm.Kind == "runtime_role" {
				valid = valid && rm.Generation == 0
			} else {
				valid = valid && rm.Generation >= 1 && rm.Generation <= 999999
			}
			expected := loginName(p, rm.Kind, rm.Generation)
			if rm.Kind == "owner" {
				expected = p.OwnerRole
			}
			if rm.Kind == "runtime_role" {
				expected = p.RuntimeRole
			}
			if !valid || mr.Role.Name != expected {
				l.Blockers = append(l.Blockers, "role provenance mismatch: "+mr.Role.Name)
				continue
			}
			roles[mr.Role.Name] = rm
			if mr.Role.Superuser || mr.Role.CreateDB || mr.Role.CreateRole || mr.Role.Replication || mr.Role.BypassRLS || ((rm.Kind == "owner" || rm.Kind == "runtime_role") && mr.Role.Login) {
				l.Blockers = append(l.Blockers, "unsafe managed role attributes: "+mr.Role.Name)
			}
			if rm.Kind == "runtime" || rm.Kind == "migration" {
				if !rm.Ready {
					l.Blockers = append(l.Blockers, "incomplete login generation requires reconciliation: "+mr.Role.Name)
				}
				l.Logins = append(l.Logins, Login{mr.Role.Name, rm.Kind, rm.Generation, mr.Role.Login})
				if rm.Kind == r.Kind && rm.Generation > maxGeneration {
					maxGeneration = rm.Generation
				}
			}
		}
		if _, ok = roles[p.OwnerRole]; !ok {
			l.Blockers = append(l.Blockers, "managed owner role is missing")
		}
		if _, ok = roles[p.RuntimeRole]; !ok {
			l.Blockers = append(l.Blockers, "managed runtime privilege role is missing")
		}
		for _, m := range s.Memberships {
			member, ok := roles[m.Member]
			valid := ok && !m.Admin && ((member.Kind == "runtime" && m.Role == p.RuntimeRole && m.Inherit && !m.Set) || (member.Kind == "migration" && m.Role == p.OwnerRole && !m.Inherit && m.Set))
			if !valid {
				l.Blockers = append(l.Blockers, "unexpected managed role membership")
			}
		}
		// Revoke must remain possible when ACL isolation has drifted. It cannot
		// add privilege. Rotation, however, must refuse to create a new login.
		if op == "rotate_login" {
			for _, db := range s.Inventory.Databases {
				if db.PublicConnect {
					l.Blockers = append(l.Blockers, "PUBLIC CONNECT isolation has drifted: "+db.Name)
				}
			}
			if maxGeneration == 0 || maxGeneration >= 999999 {
				l.Blockers = append(l.Blockers, "no safe next login generation")
			}
			l.Login = loginName(p, r.Kind, maxGeneration+1)
			l.Steps = []string{"Create the next login with a host-persisted credential; retain earlier generations.", "Switch the consumer's reference and reconnect before separately previewing retire_login."}
		} else {
			l.Login = loginName(p, r.Kind, r.Generation)
			if op == "revoke_login" {
				// Emergency disable is useful precisely when privileges or an
				// unfinished rollout are wrong. Require exact role provenance,
				// but do not require the rest of the app to be healthy.
				l.Blockers = []string{}
				if s.Inventory.Recovery || s.Inventory.ServerVersion/10000 != 17 {
					l.Blockers = append(l.Blockers, "revocation requires a writable PostgreSQL 17 primary")
				}
			}
			if _, ok := roles[l.Login]; !ok {
				l.Blockers = append(l.Blockers, "requested generation is not plugin-managed")
			}
			l.Steps = []string{"Disable new authentication, commit that change, then terminate this login's sessions. Preserve roles, grants and data."}
			if op == "retire_login" {
				l.Replacement = loginName(p, r.Kind, maxGeneration)
				if r.Generation >= maxGeneration {
					l.Blockers = append(l.Blockers, "retirement requires a newer login generation")
				}
				for _, login := range l.Logins {
					if login.Name == l.Replacement && !login.Enabled {
						l.Blockers = append(l.Blockers, "replacement login is disabled")
					}
				}
				var witness string
				// pg_read_all_stats is required for the reader to see another
				// user's application_name. The exact backend is bound to the plan.
				err = c.QueryRow(ctx, `SELECT coalesce((SELECT pid::text || ':' || extract(epoch FROM backend_start)::text FROM pg_stat_activity WHERE usename=$1 AND datname=$2 AND application_name=$3 AND backend_type='client backend' ORDER BY backend_start,pid LIMIT 1),'')`, l.Replacement, p.Database, r.Consumer).Scan(&witness)
				if err != nil {
					return l, errors.New("replacement consumer proof read failed")
				}
				l.Witness = witness
				if witness == "" {
					l.Blockers = append(l.Blockers, "no live replacement connection with the declared consumer application_name")
				}
			}
		}
	}
	l.Executable = len(l.Blockers) == 0
	// Bind desired inputs as well as observed state. Transport-only flags and
	// expected_plan are excluded so a preview can be submitted unchanged.
	r.ExpectedPlan = ""
	r.DryRun = false
	r.Acknowledged = false
	l.PlanDigest = digest(struct {
		Plan    LifecyclePlan
		State   appState
		Request LifecycleRequest
	}{l, s, r})
	return l, nil
}

func (b *postgresBackend) Preview(ctx context.Context, op string, r LifecycleRequest) (LifecyclePlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := b.connect(ctx, false, "")
	if err != nil {
		return LifecyclePlan{}, err
	}
	defer closeConn(c)
	s, err := snapshot(ctx, c, r)
	if err != nil {
		return LifecyclePlan{}, err
	}
	return lifecyclePlan(ctx, c, op, r, s)
}

func (b *postgresBackend) Execute(ctx context.Context, op string, r LifecycleRequest, issuance cerbplugin.Issuance, values map[string]string) (LifecycleResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if r.DryRun {
		return LifecycleResult{}, errors.New("dry-run cannot enter the write backend")
	}
	if err := validateLifecycle(op, r); err != nil {
		return LifecycleResult{}, err
	}
	if op == "provision_app" || op == "rotate_login" {
		if issuance.ID != r.IssuanceID || issuance.Operation != op || issuance.BindingDigest == "" {
			return LifecycleResult{}, errors.New("matching host credential issuance is required before any write")
		}
		slots := []string{"password"}
		if op == "provision_app" {
			slots = []string{"migration_password", "runtime_password"}
		}
		for _, slot := range slots {
			if !issuedPassword.MatchString(values[slot]) || issuance.References[slot] == "" {
				return LifecycleResult{}, errors.New("host issuance is missing a persisted credential")
			}
		}
	}
	c, err := b.connect(ctx, true, "")
	if err != nil {
		return LifecycleResult{}, err
	}
	defer closeConn(c)
	// Session lock spans CREATE DATABASE, which cannot run in a transaction.
	// It serializes this plugin's writers, not arbitrary superuser changes.
	var locked bool
	if c.QueryRow(ctx, `SELECT pg_try_advisory_lock(1667592802,1)`).Scan(&locked) != nil || !locked {
		return LifecycleResult{}, errors.New("another PostgreSQL lifecycle operation holds the cluster lock; re-preview later")
	}
	if err = secureWriter(ctx, c); err != nil {
		return LifecycleResult{}, err
	}
	s, err := snapshot(ctx, c, r)
	if err != nil {
		return LifecycleResult{}, err
	}
	l, err := lifecyclePlan(ctx, c, op, r, s)
	if err != nil {
		return LifecycleResult{}, err
	}
	if !l.Executable || l.PlanDigest != r.ExpectedPlan {
		return LifecycleResult{}, errors.New("plan is stale or blocked; no writes performed; inspect and dry-run again with a fresh issuance ID")
	}
	if op == "provision_app" {
		return b.provision(ctx, c, r, s.Inventory, l.App, values)
	}
	if op == "rotate_login" {
		return b.rotate(ctx, c, r, s.Inventory, l, values["password"])
	}
	return disableLogin(ctx, c, op, l)
}

func ident(s string) string { return pgx.Identifier{s}.Sanitize() }
func literal(s string) string { // only plugin-generated metadata/verifiers, never caller SQL
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
func commentSQL(object, name string, m ownership) string {
	data, _ := json.Marshal(m)
	return "COMMENT ON " + object + " " + ident(name) + " IS " + literal(string(data))
}
func phaseError(phase string) error {
	return fmt.Errorf("PostgreSQL %s failed; state may be partial; inspect before a separately reviewed recovery; never replay the issuance ID", phase)
}
