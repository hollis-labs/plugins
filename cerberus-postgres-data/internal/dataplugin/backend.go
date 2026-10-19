package dataplugin

import (
	"context"
	"encoding/json"
	"time"

	cerb "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/jackc/pgx/v5"
)

type Result struct {
	Columns []string          `json:"columns"`
	Rows    []json.RawMessage `json:"rows"`
}

func execute(ctx context.Context, cfg Config, p Projection, q cerb.DataQuery, permit cerb.DataPermit) (Result, error) {
	var out Result
	ctx, cancel := context.WithDeadline(ctx, permit.ExpiresAt)
	defer cancel()
	c, err := connectionConfig(cfg)
	if err != nil {
		return out, refused
	}
	c.ConnectTimeout = time.Second
	conn, err := pgx.ConnectConfig(ctx, c)
	if err != nil {
		return out, refused
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, refused
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A dedicated non-owner role, no memberships, and a plain base table avoid
	// owner bypass, inherited authority, views, RLS functions and custom operators.
	var safe bool
	err = tx.QueryRow(ctx, `SELECT NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
 AND current_user=session_user AND r.rolname=$1
 AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_database WHERE datname=current_database() AND datdba=r.oid)
 AND NOT has_database_privilege(current_user,current_database(),'CREATE')
 FROM pg_roles r WHERE rolname=current_user`, cfg.User).Scan(&safe)
	if err != nil || !safe {
		return out, refused
	}
	var oid uint32
	err = tx.QueryRow(ctx, `SELECT c.oid, c.relkind='r' AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND c.relowner<>r.oid
 AND NOT has_schema_privilege(current_user,n.oid,'CREATE')
 AND NOT has_table_privilege(current_user,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles r ON r.rolname=current_user
 WHERE n.nspname=$1 AND c.relname=$2`, p.Schema, p.Table).Scan(&oid, &safe)
	if err != nil || !safe {
		return out, refused
	}
	vectorSchema := ""
	if p.Vector != nil {
		err = tx.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace JOIN pg_roles r ON r.oid=e.extowner WHERE e.extname='vector' AND r.rolsuper AND NOT has_schema_privilege(current_user,n.oid,'CREATE')`).Scan(&vectorSchema)
		if err != nil {
			return out, refused
		}
	}
	required := p.requiredColumns()
	if len(required) == 0 {
		return out, refused
	}
	for _, col := range required {
		var typ, ns string
		var mod int
		err = tx.QueryRow(ctx, `SELECT t.typname,n.nspname,a.atttypmod FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid JOIN pg_namespace n ON n.oid=t.typnamespace WHERE a.attrelid=$1 AND a.attname=$2 AND a.attnum>0 AND NOT a.attisdropped AND has_column_privilege(current_user,a.attrelid,a.attnum,'SELECT')`, oid, col.Name).Scan(&typ, &ns, &mod)
		expectedNS := "pg_catalog"
		if col.Type == "vector" {
			expectedNS = vectorSchema
		}
		if err != nil || typ != col.Type || ns != expectedNS || col.Type == "vector" && mod != p.Vector.Dimensions {
			return out, refused
		}
	}
	query, args, err := p.compile(q.Parameters, permit.MaxRows, vectorSchema)
	if err != nil {
		return out, refused
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return out, refused
	}
	defer rows.Close()
	out.Columns = []string{}
	out.Rows = []json.RawMessage{}
	for _, col := range p.Columns {
		out.Columns = append(out.Columns, col.Name)
	}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return Result{}, refused
		}
		if len(values) != len(p.Columns)+1 || values[len(values)-1] != true {
			return Result{}, refused
		}
		values = values[:len(values)-1]
		b, err := json.Marshal(values)
		if err != nil {
			return Result{}, refused
		}
		out.Rows = append(out.Rows, b)
		encoded, err := json.Marshal(out)
		if err != nil || len(out.Rows) > permit.MaxRows || len(encoded) > permit.MaxBytes {
			return Result{}, refused
		}
	}
	if rows.Err() != nil || ctx.Err() != nil || tx.Commit(ctx) != nil || permit.Validate(q, time.Now()) != nil {
		return Result{}, refused
	}
	return out, nil
}
