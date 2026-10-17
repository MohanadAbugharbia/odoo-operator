/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package pg is the PostgreSQL side of backups and restores: the SQL the
// backup Jobs run through pgx, and picking the pg_dump and psql that match
// the server.
package pg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/odooconf"
)

// DSN is the connection URL for database with the odoo.conf settings.
func DSN(c odooconf.Config, database string) string {
	q := url.Values{}
	q.Set("sslmode", c.SSLMode)
	q.Set("connect_timeout", "10")
	q.Set("application_name", "odoo-operator-backup")
	host := c.Host
	if host == "" {
		host = "localhost"
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     net.JoinHostPort(host, strconv.Itoa(c.Port)),
		Path:     "/" + database,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// Connect opens a connection to database.
func Connect(ctx context.Context, c odooconf.Config, database string) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, DSN(c, database))
	if err != nil {
		return nil, fmt.Errorf("connect to database %q on %s as %s: %w", database, c.Host, c.User, err)
	}
	return conn, nil
}

// Ident quotes an identifier.
func Ident(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// Literal quotes a string literal.
func Literal(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	return string(append(out, '\''))
}

// ServerVersionNum returns server_version_num.
func ServerVersionNum(ctx context.Context, conn *pgx.Conn) (int, error) {
	var s string
	if err := conn.QueryRow(ctx, "SHOW server_version_num").Scan(&s); err != nil {
		return 0, fmt.Errorf("read server_version_num: %w", err)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("server_version_num %q: %w", s, err)
	}
	return n, nil
}

// ---- client binaries ----------------------------------------------------

// Clients finds pg_dump and psql for a server. The backup image installs one
// client per supported major under BinDirs (formatted with the major).
type Clients struct {
	// BinDirTemplate holds %d for the major, such as /usr/libexec/postgresql%d.
	BinDirTemplate string
	// Majors are the client majors to look for, lowest first.
	Majors []int
}

// Pick returns the folder of the lowest installed client major that is not
// older than the server's: pg_dump refuses to dump a newer server, and the
// same major restores most faithfully. With no template it returns "" and
// the tools are taken from PATH.
func (c Clients) Pick(serverMajor int) (string, error) {
	if c.BinDirTemplate == "" {
		return "", nil
	}
	majors := append([]int(nil), c.Majors...)
	sort.Ints(majors)
	var installed []int
	for _, m := range majors {
		dir := fmt.Sprintf(c.BinDirTemplate, m)
		if _, err := os.Stat(filepath.Join(dir, "pg_dump")); err != nil {
			continue
		}
		installed = append(installed, m)
		if m >= serverMajor {
			return dir, nil
		}
	}
	return "", fmt.Errorf("no PostgreSQL client for server version %d (installed: %v)", serverMajor, installed)
}

// Major is the major of a server_version_num.
func Major(serverVersionNum int) int {
	return serverVersionNum / 10000
}

// Tool is the path of a client tool in dir ("" = PATH).
func Tool(dir, name string) string {
	if dir == "" {
		return name
	}
	return filepath.Join(dir, name)
}

// ---- backup side ----------------------------------------------------------

// Querier is a connection or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Snapshot is an open REPEATABLE READ transaction whose snapshot pg_dump
// imports, so the dump and everything read through Tx see the same data.
type Snapshot struct {
	Conn *pgx.Conn
	Tx   pgx.Tx
	ID   string
	Time time.Time
}

// ExportSnapshot opens a read-only REPEATABLE READ transaction and exports
// its snapshot. No table lock is taken.
func ExportSnapshot(ctx context.Context, conn *pgx.Conn) (*Snapshot, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin snapshot transaction: %w", err)
	}
	s := &Snapshot{Conn: conn, Tx: tx}
	if err := tx.QueryRow(ctx, "SELECT pg_export_snapshot(), now()").Scan(&s.ID, &s.Time); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("export snapshot: %w", err)
	}
	return s, nil
}

// Close ends the snapshot transaction.
func (s *Snapshot) Close(ctx context.Context) {
	_ = s.Tx.Rollback(ctx)
}

// StoreNames returns every distinct ir_attachment.store_fname, the
// filestore files the database refers to.
func StoreNames(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.Query(ctx,
		"SELECT DISTINCT store_fname FROM ir_attachment WHERE store_fname IS NOT NULL AND store_fname <> '' ORDER BY 1")
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Tables returns the schema-qualified ordinary tables of the database,
// outside the system schemas and extensions.
func Tables(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT format('%I.%I', n.nspname, c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p')
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg\_toast%'
		  AND n.nspname NOT LIKE 'pg\_temp%'
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d
		                  WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// RowCounts counts the rows of every table.
func RowCounts(ctx context.Context, q Querier) (map[string]int64, error) {
	tables, err := Tables(ctx, q)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(tables))
	for _, t := range tables {
		var n int64
		// t comes from format('%I.%I'), so it is already quoted.
		if err := q.QueryRow(ctx, "SELECT count(*) FROM "+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("count rows of %s: %w", t, err)
		}
		counts[t] = n
	}
	return counts, nil
}

// InstalledModules returns the installed modules and their versions.
func InstalledModules(ctx context.Context, q Querier) (map[string]string, error) {
	rows, err := q.Query(ctx,
		"SELECT name, COALESCE(latest_version, '') FROM ir_module_module WHERE state = 'installed'")
	if err != nil {
		return nil, fmt.Errorf("list installed modules: %w", err)
	}
	modules := map[string]string{}
	var name, version string
	_, err = pgx.ForEachRow(rows, []any{&name, &version}, func() error {
		modules[name] = version
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list installed modules: %w", err)
	}
	return modules, nil
}

// ---- Odoo's filestore cleanup -----------------------------------------------

// Autovacuum is the state of Odoo's base.autovacuum_job, which deletes
// unreferenced filestore files.
type Autovacuum struct {
	Found   bool
	Active  bool
	Running bool
	// NextCall is ir_cron.nextcall, in UTC.
	NextCall time.Time
	// Now is the database server's clock, in UTC.
	Now time.Time
}

// CheckAutovacuum reads the autovacuum cron. Running is detected the way
// Odoo's cron runner itself does it: a running job holds a lock on its
// ir_cron row, so SKIP LOCKED does not return it. The probe's own lock lasts
// only for its short transaction.
func CheckAutovacuum(ctx context.Context, conn *pgx.Conn) (Autovacuum, error) {
	var av Autovacuum
	tx, err := conn.Begin(ctx)
	if err != nil {
		return av, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	err = tx.QueryRow(ctx, `
		SELECT c.id, c.active, c.nextcall, (now() AT TIME ZONE 'UTC')
		FROM ir_cron c JOIN ir_model_data d ON d.model = 'ir.cron' AND d.res_id = c.id
		WHERE d.module = 'base' AND d.name = 'autovacuum_job'`).Scan(&id, &av.Active, &av.NextCall, &av.Now)
	if errors.Is(err, pgx.ErrNoRows) {
		return av, nil
	}
	if err != nil {
		return av, fmt.Errorf("read the autovacuum cron: %w", err)
	}
	av.Found = true
	var got int64
	err = tx.QueryRow(ctx, "SELECT id FROM ir_cron WHERE id = $1 FOR NO KEY UPDATE SKIP LOCKED", id).Scan(&got)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		av.Running = true
	case err != nil:
		return av, fmt.Errorf("probe the autovacuum cron lock: %w", err)
	}
	return av, nil
}

// ShouldWait says whether a backup should wait for the cleanup: it is
// running, or due within window. A job overdue by more than an hour is taken
// as not running at all (no cron worker), so it never blocks backups.
func (a Autovacuum) ShouldWait(window time.Duration) (bool, string) {
	switch {
	case !a.Found:
		return false, ""
	case a.Running:
		return true, "Odoo's filestore cleanup (base.autovacuum_job) is running"
	case !a.Active:
		return false, ""
	case a.NextCall.After(a.Now.Add(-time.Hour)) && a.NextCall.Before(a.Now.Add(window)):
		return true, fmt.Sprintf("Odoo's filestore cleanup (base.autovacuum_job) is due at %s UTC", a.NextCall.Format("15:04"))
	}
	return false, ""
}

// ---- restore side -----------------------------------------------------------

// DatabaseInfo is what a restore needs to know about a database.
type DatabaseInfo struct {
	Exists   bool
	Encoding string
	Collate  string
	Ctype    string
	Comment  *string
}

// Database reads a database's settings from the maintenance connection.
func Database(ctx context.Context, conn *pgx.Conn, name string) (DatabaseInfo, error) {
	info := DatabaseInfo{}
	err := conn.QueryRow(ctx, `
		SELECT pg_encoding_to_char(encoding), datcollate, datctype, shobj_description(oid, 'pg_database')
		FROM pg_database WHERE datname = $1`, name).Scan(&info.Encoding, &info.Collate, &info.Ctype, &info.Comment)
	if errors.Is(err, pgx.ErrNoRows) {
		return info, nil
	}
	if err != nil {
		return info, fmt.Errorf("read database %q: %w", name, err)
	}
	info.Exists = true
	return info, nil
}

// Exists reports whether a database exists.
func Exists(ctx context.Context, conn *pgx.Conn, name string) (bool, error) {
	info, err := Database(ctx, conn, name)
	return info.Exists, err
}

// CreateLike creates name with like's encoding and collation, or Odoo's
// defaults (unicode, C) when like does not exist.
func CreateLike(ctx context.Context, conn *pgx.Conn, name string, like DatabaseInfo) error {
	encoding, collate, ctype := "UTF8", "C", "C"
	if like.Exists {
		encoding, collate, ctype = like.Encoding, like.Collate, like.Ctype
	}
	ddl := fmt.Sprintf("CREATE DATABASE %s ENCODING %s LC_COLLATE %s LC_CTYPE %s TEMPLATE template0",
		Ident(name), Literal(encoding), Literal(collate), Literal(ctype))
	if _, err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("create database %q: %w", name, err)
	}
	return nil
}

// Drop drops a database, ending its sessions.
func Drop(ctx context.Context, conn *pgx.Conn, name string) error {
	if err := Terminate(ctx, conn, name); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+Ident(name)); err != nil {
		return fmt.Errorf("drop database %q: %w", name, err)
	}
	return nil
}

// Terminate ends the other client sessions on the databases that the
// current role may end (its own and its member roles'). Autovacuum workers
// are left alone: DROP and RENAME DATABASE stop them themselves, and a
// non-superuser may not. Sessions of other roles make the following DROP or
// RENAME fail with "being accessed by other users", which names the problem.
func Terminate(ctx context.Context, conn *pgx.Conn, names ...string) error {
	_, err := conn.Exec(ctx, `
		SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE datname = ANY($1) AND pid <> pg_backend_pid() AND backend_type = 'client backend'
		  AND pg_has_role(current_user, usesysid, 'MEMBER')`, names)
	if err != nil {
		return fmt.Errorf("end sessions on %v: %w", names, err)
	}
	return nil
}

// Rename is one ALTER DATABASE ... RENAME.
type Rename struct{ From, To string }

// SwapNames renames databases in one transaction, so either all renames
// happen or none. The comment, when not nil, is set on the database renamed
// last (the one that becomes live), so the operator still recognises it.
func SwapNames(ctx context.Context, conn *pgx.Conn, renames []Rename, comment *string) error {
	names := make([]string, 0, 2*len(renames))
	for _, r := range renames {
		names = append(names, r.From, r.To)
	}
	if err := Terminate(ctx, conn, names...); err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, r := range renames {
		if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s RENAME TO %s", Ident(r.From), Ident(r.To))); err != nil {
			return fmt.Errorf("rename database %q to %q: %w", r.From, r.To, err)
		}
	}
	if comment != nil && len(renames) > 0 {
		last := renames[len(renames)-1].To
		if _, err := tx.Exec(ctx, fmt.Sprintf("COMMENT ON DATABASE %s IS %s", Ident(last), Literal(*comment))); err != nil {
			return fmt.Errorf("comment on database %q: %w", last, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit the renames: %w", err)
	}
	return nil
}

// SetComment sets a database's comment (nil clears it).
func SetComment(ctx context.Context, conn *pgx.Conn, name string, comment *string) error {
	value := "NULL"
	if comment != nil {
		value = Literal(*comment)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("COMMENT ON DATABASE %s IS %s", Ident(name), value)); err != nil {
		return fmt.Errorf("comment on database %q: %w", name, err)
	}
	return nil
}

// BaseVersion returns base's installed version in an Odoo database, or ""
// when it is not an Odoo database.
func BaseVersion(ctx context.Context, conn Querier) (string, error) {
	var version *string
	err := conn.QueryRow(ctx,
		"SELECT latest_version FROM ir_module_module WHERE name = 'base' AND state = 'installed'").Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		var pgErr interface{ SQLState() string }
		if errors.As(err, &pgErr) && pgErr.SQLState() == "42P01" { // undefined_table: not an Odoo database
			return "", nil
		}
		return "", fmt.Errorf("read the base module version: %w", err)
	}
	if version == nil {
		return "", nil
	}
	return *version, nil
}
