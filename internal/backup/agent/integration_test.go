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

package agent

// Real-service tests: a PostgreSQL server, the restic binary and the
// PostgreSQL client tools of the server's major. They skip without
// TEST_PG_HOST, so `make test` still runs offline.
//
//	TEST_PG_HOST=127.0.0.1 TEST_PG_PORT=5432 TEST_PG_USER=postgres \
//	TEST_PG_PASSWORD=postgres go test ./internal/backup/agent/
//
// TEST_RESTIC_BIN picks the restic binary (default: restic on PATH) and
// ODOO_BACKUP_PG_BIN_TEMPLATE the client tools (default: PATH), as in the
// backup image. Each test runs as its own non-superuser role with CREATEDB,
// as an Odoo database user is.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	mrand "math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/manifest"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/odooconf"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/pg"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

const (
	itNamespace  = "shop"
	itDeployment = "my-odoo"
	itOwnerTag   = "odoo-operator:shop/my-odoo"
)

// itServer is the server the tests run against, as its superuser.
type itServer struct {
	admin  odooconf.Config
	maint  string
	restic string
	majors []int
}

func newServer(t *testing.T) itServer {
	t.Helper()
	host := os.Getenv("TEST_PG_HOST")
	if host == "" {
		t.Skip("TEST_PG_HOST is not set, skipping the real-service backup tests")
	}
	port := 5432
	if raw := os.Getenv("TEST_PG_PORT"); raw != "" {
		var err error
		if port, err = strconv.Atoi(raw); err != nil {
			t.Fatalf("TEST_PG_PORT=%q: %v", raw, err)
		}
	}
	user := os.Getenv("TEST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	maint := os.Getenv("TEST_PG_MAINTENANCE_DB")
	if maint == "" {
		maint = "postgres"
	}
	bin := os.Getenv("TEST_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Fatalf("TEST_PG_HOST is set but restic is not on PATH (set TEST_RESTIC_BIN)")
		}
	}
	return itServer{
		admin: odooconf.Config{
			Host: host, Port: port, User: user, Password: os.Getenv("TEST_PG_PASSWORD"),
			Name: maint, SSLMode: "disable",
		},
		maint:  maint,
		restic: bin,
		majors: []int{13, 14, 15, 16, 17, 18, 19},
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// fixture is one instance: a role, its live database and filestore, and a
// restic repository.
type fixture struct {
	t        *testing.T
	srv      itServer
	cfg      odooconf.Config
	db       string
	dataRoot string
	repo     string
	env      *Env
	clock    time.Time
	logs     *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	srv := newServer(t)
	ctx := context.Background()
	id := randomHex(t, 4)
	role := "it_odoo_" + id
	password := "pw-" + id
	f := &fixture{
		t:        t,
		srv:      srv,
		db:       "it_" + id,
		dataRoot: t.TempDir(),
		repo:     filepath.Join(t.TempDir(), "repo"),
		clock:    time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		logs:     &bytes.Buffer{},
	}
	admin := f.adminConn()
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN CREATEDB PASSWORD %s", pg.Ident(role), pg.Literal(password))); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() { f.dropAll(role) })
	f.cfg = odooconf.Config{
		Host: srv.admin.Host, Port: srv.admin.Port, User: role, Password: password,
		Name: f.db, SSLMode: "disable",
	}
	// Created the way the operator creates it: by the Odoo role, tagged.
	maint := f.conn(srv.maint)
	if _, err := maint.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s ENCODING 'unicode' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0", pg.Ident(f.db))); err != nil {
		t.Fatalf("create database: %v", err)
	}
	if _, err := maint.Exec(ctx, fmt.Sprintf("COMMENT ON DATABASE %s IS %s", pg.Ident(f.db), pg.Literal(itOwnerTag))); err != nil {
		t.Fatal(err)
	}
	f.env = &Env{
		Config:        f.cfg,
		MaintenanceDB: srv.maint,
		Restic: &restic.Restic{
			Bin: srv.restic,
			Env: []string{
				"RESTIC_REPOSITORY=" + f.repo,
				"RESTIC_PASSWORD=it-" + id,
				"RESTIC_CACHE_DIR=" + filepath.Join(t.TempDir(), "cache"),
			},
			RetryLock: time.Minute,
			Log:       f.logs,
		},
		Clients: pg.Clients{BinDirTemplate: os.Getenv("ODOO_BACKUP_PG_BIN_TEMPLATE"), Majors: srv.majors},
		Log:     NewLogger(f.logs),
		Now:     func() time.Time { return f.clock },
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("agent log:\n%s", f.logs.String())
		}
	})
	return f
}

func (f *fixture) adminConn() *pgx.Conn {
	f.t.Helper()
	conn, err := pg.Connect(context.Background(), f.srv.admin, f.srv.maint)
	if err != nil {
		f.t.Fatalf("connect as admin: %v", err)
	}
	f.t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// conn connects to a database as the instance's role.
func (f *fixture) conn(database string) *pgx.Conn {
	f.t.Helper()
	conn, err := pg.Connect(context.Background(), f.cfg, database)
	if err != nil {
		f.t.Fatalf("connect to %s: %v", database, err)
	}
	f.t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func (f *fixture) exec(database, sql string, args ...any) {
	f.t.Helper()
	conn, err := pg.Connect(context.Background(), f.cfg, database)
	if err != nil {
		f.t.Fatalf("connect to %s: %v", database, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("exec on %s: %v\n%s", database, err, sql)
	}
}

func (f *fixture) dropAll(role string) {
	ctx := context.Background()
	conn, err := pg.Connect(ctx, f.srv.admin, f.srv.maint)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, "SELECT datname FROM pg_database WHERE datname LIKE $1", f.db+"%")
	if err != nil {
		return
	}
	names, _ := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, n := range names {
		_ = pg.Drop(ctx, conn, n)
	}
	_, _ = conn.Exec(ctx, "DROP ROLE IF EXISTS "+pg.Ident(role))
}

// databases lists this fixture's databases.
func (f *fixture) databases() []string {
	f.t.Helper()
	conn := f.adminConn()
	rows, err := conn.Query(context.Background(), "SELECT datname FROM pg_database WHERE datname LIKE $1 ORDER BY 1", f.db+"%")
	if err != nil {
		f.t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	return names
}

func (f *fixture) comment(database string) string {
	f.t.Helper()
	conn := f.adminConn()
	var c *string
	if err := conn.QueryRow(context.Background(),
		"SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1", database).Scan(&c); err != nil {
		f.t.Fatalf("comment of %s: %v", database, err)
	}
	if c == nil {
		return ""
	}
	return *c
}

func (f *fixture) filestore(database string) string {
	return filepath.Join(f.dataRoot, layout.FilestorePath(database))
}

// seedSQL is an Odoo-shaped database with the data types, objects and
// encodings a dump has to carry faithfully.
const seedSQL = `
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE TABLE ir_module_module (id serial PRIMARY KEY, name varchar NOT NULL UNIQUE, latest_version varchar, state varchar);
INSERT INTO ir_module_module (name, latest_version, state) VALUES
  ('base','18.0.1.3','installed'), ('web','18.0.1.0','installed'),
  ('account','18.0.1.2','installed'), ('sale','18.0.1.0','uninstalled');
CREATE TABLE ir_model_data (id serial PRIMARY KEY, module varchar, name varchar, model varchar, res_id int);
CREATE TABLE ir_cron (id serial PRIMARY KEY, active bool, nextcall timestamp);
INSERT INTO ir_cron (active, nextcall) VALUES (true, (now() at time zone 'utc') + interval '1 day');
INSERT INTO ir_model_data (module, name, model, res_id) VALUES ('base', 'autovacuum_job', 'ir.cron', 1);
CREATE TABLE ir_attachment (id serial PRIMARY KEY, name varchar, store_fname varchar, db_datas bytea, checksum varchar, mimetype varchar);
CREATE TABLE res_partner (
  id serial PRIMARY KEY, name jsonb NOT NULL, amount numeric(20,6), created timestamptz DEFAULT now(),
  birthday date, tags text[], doc tsvector, uid uuid, blob bytea, note text, ratio double precision,
  active boolean DEFAULT true, parent_id int REFERENCES res_partner(id) ON DELETE SET NULL);
COMMENT ON TABLE res_partner IS 'Contacts — جهات الاتصال';
INSERT INTO res_partner (name, amount, birthday, tags, doc, uid, blob, note, ratio)
SELECT jsonb_build_object('en_US', 'Partner ' || g, 'ar_001', 'شريك ' || g, 'emoji', '🧾✓'),
       g * 1.5, date '1990-01-01' + g, ARRAY['a', 'b''c', 'd"e', NULL], to_tsvector('simple', 'hello world ' || g),
       md5(g::text)::uuid, decode(md5(g::text), 'hex'),
       E'line1\nline2\ttab ''quote'' \\ backslash é'::text, g / 7.0
FROM generate_series(1, 1500) g;
UPDATE res_partner SET parent_id = id - 1 WHERE id > 1 AND id % 3 = 0;
INSERT INTO res_partner (name, note, amount) VALUES ('{"en_US": null}', NULL, NULL), ('{}', '', 0);
CREATE INDEX res_partner_note_trgm ON res_partner USING gin (note gin_trgm_ops);
CREATE INDEX res_partner_name_trgm ON res_partner USING gin ((name->>'en_US') gin_trgm_ops);
CREATE VIEW partner_view AS SELECT id, name->>'en_US' AS name FROM res_partner WHERE active;
CREATE MATERIALIZED VIEW partner_count AS SELECT count(*) AS n FROM res_partner;
CREATE SEQUENCE custom_seq START 42;
SELECT nextval('custom_seq'), nextval('custom_seq');
CREATE FUNCTION touch_note() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.note := coalesce(NEW.note, '') || '!'; RETURN NEW; END $$;
CREATE TRIGGER res_partner_touch BEFORE UPDATE OF ratio ON res_partner FOR EACH ROW EXECUTE FUNCTION touch_note();
CREATE TABLE "Mixed Case ""Quoted"" Table" ("Weird Col" text, "عمود" int);
INSERT INTO "Mixed Case ""Quoted"" Table" VALUES ('x', 1), (E'tab\there', 2);
CREATE SCHEMA extra;
CREATE TABLE extra.audit (id bigserial PRIMARY KEY, payload jsonb, at timestamp);
INSERT INTO extra.audit (payload, at) SELECT jsonb_build_object('i', g), timestamp '2026-01-01' + g * interval '1 minute' FROM generate_series(1, 300) g;
CREATE TABLE measurement (logdate date NOT NULL, value int) PARTITION BY RANGE (logdate);
CREATE TABLE measurement_2026 PARTITION OF measurement FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
INSERT INTO measurement SELECT date '2026-01-01' + g, g FROM generate_series(0, 200) g;
CREATE TABLE big_text (id int, body text);
INSERT INTO big_text SELECT g, repeat(md5(g::text), 2000) FROM generate_series(1, 20) g;
`

// seed fills the live database and writes n attachments to the filestore.
func (f *fixture) seed(n int) {
	f.t.Helper()
	f.exec(f.db, seedSQL)
	f.exec(f.db, "SELECT lo_from_bytea(0, convert_to('large object content ✓', 'UTF8'))")
	for i := 0; i < n; i++ {
		f.addAttachment(fmt.Sprintf("file-%d.bin", i), randomContent(i))
	}
}

func randomContent(seed int) []byte {
	r := mrand.New(mrand.NewPCG(uint64(seed), 7))
	size := 1 + r.IntN(64*1024)
	if seed%50 == 0 {
		size = 2 << 20 // a few larger files
	}
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return b
}

// addAttachment writes a file the way Odoo does (named by its sha1) and the
// ir_attachment row referring to it.
func (f *fixture) addAttachment(name string, content []byte) {
	f.t.Helper()
	sum := sha1.Sum(content)
	sha := hex.EncodeToString(sum[:])
	storeName := sha[:2] + "/" + sha
	path := filepath.Join(f.filestore(f.db), storeName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.exec(f.db, "INSERT INTO ir_attachment (name, store_fname, checksum, mimetype) VALUES ($1, $2, $3, 'application/octet-stream')",
		name, storeName, sha)
}

// fingerprint describes a database's content: every table's rows, the
// sequences, large objects, views, indexes, functions, triggers, extensions
// and comments.
func (f *fixture) fingerprint(database string) map[string]string {
	f.t.Helper()
	ctx := context.Background()
	conn := f.conn(database)
	out := map[string]string{}
	rows, err := conn.Query(ctx, `
		SELECT format('%I.%I', n.nspname, c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'
		ORDER BY 1`)
	if err != nil {
		f.t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	for _, tbl := range tables {
		var sum *string
		var n int64
		if err := conn.QueryRow(ctx, fmt.Sprintf(
			"SELECT md5(string_agg(t::text, E'\\n' ORDER BY t::text)), count(*) FROM %s t", tbl)).Scan(&sum, &n); err != nil {
			f.t.Fatalf("fingerprint %s: %v", tbl, err)
		}
		out["table "+tbl] = fmt.Sprintf("%d rows %v", n, deref(sum))
	}
	queries := map[string]string{
		"sequences":     `SELECT string_agg(format('%s.%s=%s', schemaname, sequencename, coalesce(last_value::text, 'unused')), ',' ORDER BY schemaname, sequencename) FROM pg_sequences`,
		"large objects": `SELECT string_agg(md5(lo_get(oid)), ',' ORDER BY md5(lo_get(oid))) FROM pg_largeobject_metadata`,
		"views":         `SELECT string_agg(viewname || ':' || md5(definition), ',' ORDER BY viewname) FROM pg_views WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`,
		"indexes":       `SELECT string_agg(indexdef, ';' ORDER BY indexdef) FROM pg_indexes WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`,
		"functions":     `SELECT string_agg(p.proname || md5(p.prosrc), ',' ORDER BY p.proname) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public' AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')`,
		// Materialized views only by definition: a restore refreshes them,
		// so their rows are as of the restore, as with pg_restore.
		"matviews":   `SELECT string_agg(matviewname || ':' || md5(definition), ',' ORDER BY matviewname) FROM pg_matviews`,
		"triggers":   `SELECT string_agg(tgname, ',' ORDER BY tgname) FROM pg_trigger WHERE NOT tgisinternal`,
		"extensions": `SELECT string_agg(extname, ',' ORDER BY extname) FROM pg_extension`,
		"comments":   `SELECT string_agg(description, ',' ORDER BY description) FROM pg_description d JOIN pg_class c ON c.oid = d.objoid AND d.classoid = 'pg_class'::regclass`,
		"encoding":   `SELECT pg_encoding_to_char(encoding) || datcollate || datctype FROM pg_database WHERE datname = current_database()`,
	}
	for k, q := range queries {
		var v *string
		if err := conn.QueryRow(ctx, q).Scan(&v); err != nil {
			f.t.Fatalf("fingerprint %s: %v", k, err)
		}
		out[k] = deref(v)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// files maps every file under dir to its sha256.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == dir {
				return filepath.SkipDir
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffMaps(want, got map[string]string) string {
	var diff []string
	for k, v := range want {
		if g, ok := got[k]; !ok {
			diff = append(diff, "missing "+k)
		} else if g != v {
			diff = append(diff, fmt.Sprintf("%s: %.80s != %.80s", k, g, v))
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			diff = append(diff, "unexpected "+k)
		}
	}
	sort.Strings(diff)
	if len(diff) > 10 {
		diff = append(diff[:10], fmt.Sprintf("... %d more", len(diff)-10))
	}
	return strings.Join(diff, "\n")
}

// state is a database and filestore folder's content.
type state struct {
	db    map[string]string
	files map[string]string
}

func (f *fixture) state(name string) state {
	return state{db: f.fingerprint(name), files: files(f.t, f.filestore(name))}
}

func (f *fixture) assertState(name string, want state, what string) {
	f.t.Helper()
	got := f.state(name)
	if d := diffMaps(want.db, got.db); d != "" {
		f.t.Fatalf("%s: database %s differs:\n%s", what, name, d)
	}
	if d := diffMaps(want.files, got.files); d != "" {
		f.t.Fatalf("%s: filestore %s differs:\n%s", what, name, d)
	}
}

// backup takes a backup the way the Job does: the instance's filestore is
// mounted at <root>/filestore, read-only. The test links its files there.
func (f *fixture) backup(name string, opts ...func(*BackupOptions)) result.Result {
	f.t.Helper()
	root := f.t.TempDir()
	if err := linkTree(f.filestore(f.db), filepath.Join(root, layout.FilestoreDir)); err != nil {
		f.t.Fatal(err)
	}
	o := BackupOptions{
		Root: root, Namespace: itNamespace, Deployment: itDeployment, Backup: name,
		Kind: "manual", Image: "odoo:18.0-20260301", UpgradeToken: "tok-1", Consistency: "Online",
		Version: "test",
	}
	for _, opt := range opts {
		opt(&o)
	}
	return Backup(context.Background(), f.env, o)
}

func linkTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == src {
				return filepath.SkipDir
			}
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return os.Link(path, target)
	})
}

func (f *fixture) mustBackup(name string, opts ...func(*BackupOptions)) result.Result {
	f.t.Helper()
	res := f.backup(name, opts...)
	if !res.OK || res.Deferred {
		f.t.Fatalf("backup %s: %+v", name, res)
	}
	return res
}

// nextSuffix advances the clock and returns a fresh restore suffix.
func (f *fixture) nextSuffix() string {
	f.clock = f.clock.Add(time.Minute)
	return layout.Suffix(f.clock)
}

func (f *fixture) restore(snapshot, suffix string) result.Result {
	f.t.Helper()
	return Restore(context.Background(), f.env, RestoreOptions{
		Mode: ModeRestore, Suffix: suffix, Snapshot: snapshot, DataRoot: f.dataRoot,
		ExpectModules: []string{"base", "web", "sale"}, CheckSample: 50,
	})
}

func (f *fixture) undo(undoSuffix, suffix string) result.Result {
	f.t.Helper()
	return Restore(context.Background(), f.env, RestoreOptions{
		Mode: ModeUndo, Suffix: suffix, UndoSuffix: undoSuffix, DataRoot: f.dataRoot,
		ExpectModules: []string{"base", "web"},
	})
}

func (f *fixture) cleanup(suffix string) result.Result {
	f.t.Helper()
	return Cleanup(context.Background(), f.env, f.dataRoot, suffix)
}

// mutate changes the live database and filestore after a backup.
func (f *fixture) mutate() {
	f.t.Helper()
	f.exec(f.db, "INSERT INTO res_partner (name) VALUES ('{\"en_US\": \"after the backup\"}')")
	f.exec(f.db, "DELETE FROM extra.audit WHERE id < 50")
	f.exec(f.db, "UPDATE res_partner SET ratio = 0 WHERE id < 10")
	f.exec(f.db, "SELECT nextval('custom_seq')")
	f.addAttachment("after.txt", []byte("written after the backup"))
}

// ---------------------------------------------------------------------------

func TestBackupRestoreRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.seed(120)
	before := f.state(f.db)

	res := f.mustBackup("first")
	if res.SnapshotID == "" || res.DatabaseBytes == 0 || res.FilestoreFiles != 120 || res.FilestoreBytes == 0 {
		t.Fatalf("backup result: %+v", res)
	}
	if res.OdooVersion != "18.0" || res.MissingFiles != 0 || res.RepairedFiles != 0 || len(res.Warnings) != 0 {
		t.Fatalf("backup result: %+v", res)
	}
	if !strings.Contains(res.PgServerVersion, ".") {
		t.Fatalf("pgServerVersion %q", res.PgServerVersion)
	}

	f.mutate()
	mutated := f.state(f.db)

	suffix := f.nextSuffix()
	rr := f.restore(res.SnapshotID, suffix)
	if !rr.OK || !rr.LiveChanged || !rr.PreviousKept {
		t.Fatalf("restore: %+v", rr)
	}
	if rr.Image != "odoo:18.0-20260301" || rr.UpgradeToken != "tok-1" || rr.OdooVersion != "18.0" {
		t.Fatalf("restore did not return the backup's image: %+v", rr)
	}
	if strings.Join(rr.MissingModules, ",") != "sale" {
		t.Fatalf("missing modules: %v", rr.MissingModules)
	}
	f.assertState(f.db, before, "after the restore")
	prev := layout.PrevName(f.db, suffix)
	f.assertState(prev, mutated, "the replaced data")
	if c := f.comment(f.db); c != itOwnerTag {
		t.Fatalf("the restored database lost the owner tag: %q", c)
	}
	if got := f.databases(); strings.Join(got, ",") != strings.Join(sorted(f.db, prev), ",") {
		t.Fatalf("databases after the restore: %v", got)
	}
	if _, err := os.Stat(filepath.Join(f.dataRoot, layout.StagingDir(suffix))); !os.IsNotExist(err) {
		t.Fatalf("staging folder left behind: %v", err)
	}

	// The cleanup deletes the replaced data and nothing else.
	if cr := f.cleanup(suffix); !cr.OK {
		t.Fatalf("cleanup: %+v", cr)
	}
	if got := f.databases(); strings.Join(got, ",") != f.db {
		t.Fatalf("databases after the cleanup: %v", got)
	}
	if _, err := os.Stat(f.filestore(prev)); !os.IsNotExist(err) {
		t.Fatalf("replaced filestore still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dataRoot, layout.JournalFile(suffix))); !os.IsNotExist(err) {
		t.Fatalf("journal still there: %v", err)
	}
	f.assertState(f.db, before, "after the cleanup")
	// Running it again is harmless.
	if cr := f.cleanup(suffix); !cr.OK {
		t.Fatalf("second cleanup: %+v", cr)
	}
}

func sorted(s ...string) []string {
	sort.Strings(s)
	return s
}

func TestBackupSnapshotLayout(t *testing.T) {
	f := newFixture(t)
	f.seed(5)
	res := f.mustBackup("layout", func(o *BackupOptions) { o.Kind = "scheduled" })

	snaps, err := f.env.Restic.Snapshots(context.Background(), layout.Host(itNamespace, itDeployment),
		layout.TagDeployment+layout.Host(itNamespace, itDeployment))
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots: %v %v", snaps, err)
	}
	s := snaps[0]
	for _, tag := range layout.Tags(itNamespace, itDeployment, "layout", "scheduled") {
		if !contains(s.Tags, tag) {
			t.Fatalf("snapshot tags %v lack %s", s.Tags, tag)
		}
	}
	var buf bytes.Buffer
	if err := f.env.Restic.Dump(context.Background(), res.SnapshotID, filepath.Join(s.Paths[0], layout.ManifestFile), &buf); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	// What Odoo's own restore reads.
	if m.OdooDump != "1" || m.DBName != f.db || m.Version != "18.0" || m.Modules["account"] != "18.0.1.2" || m.PgVersion == "" {
		t.Fatalf("manifest: %+v", m)
	}
	op := m.Operator
	if op == nil || op.Namespace != itNamespace || op.Deployment != itDeployment || op.Backup != "layout" ||
		op.Kind != "scheduled" || op.Image != "odoo:18.0-20260301" || op.DumpSHA256 == "" || op.Files.Referenced != 5 ||
		op.RowCounts["public.res_partner"] != 1502 {
		t.Fatalf("operator manifest: %+v", op)
	}
	// dump.sql is plain SQL a psql of the same major loads.
	buf.Reset()
	if err := f.env.Restic.Dump(context.Background(), res.SnapshotID, filepath.Join(s.Paths[0], layout.DumpFile), &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "PostgreSQL database dump") || strings.Contains(buf.String(), "OWNER TO") {
		t.Fatalf("dump.sql is not a plain dump without owners")
	}
}

func TestBackupRepairsFromPreviousBackup(t *testing.T) {
	f := newFixture(t)
	f.seed(10)
	before := f.state(f.db)
	f.mustBackup("first")

	// A file vanishes from the filestore while the database still refers to it.
	var lostName string
	for name := range before.files {
		lostName = name
		break
	}
	if err := os.Remove(filepath.Join(f.filestore(f.db), lostName)); err != nil {
		t.Fatal(err)
	}
	res := f.mustBackup("second")
	if res.RepairedFiles != 1 || res.MissingFiles != 0 {
		t.Fatalf("expected one repaired file: %+v", res)
	}

	suffix := f.nextSuffix()
	if rr := f.restore(res.SnapshotID, suffix); !rr.OK {
		t.Fatalf("restore: %+v", rr)
	}
	f.assertState(f.db, before, "restored from the repaired backup")
}

func TestBackupReportsFilesMissingEverywhere(t *testing.T) {
	f := newFixture(t)
	f.seed(4)
	f.exec(f.db, "INSERT INTO ir_attachment (name, store_fname) VALUES ('gone', 'ab/abcdef0123456789abcdef0123456789abcdef01'), ('evil', '../../etc/passwd')")
	res := f.mustBackup("first")
	if res.MissingFiles != 2 || len(res.Warnings) == 0 || !contains(res.MissingNames, "ab/abcdef0123456789abcdef0123456789abcdef01") ||
		!contains(res.MissingNames, "../../etc/passwd") {
		t.Fatalf("expected the missing files to be reported: %+v", res)
	}
	before := f.state(f.db)
	f.mutate()
	suffix := f.nextSuffix()
	rr := f.restore(res.SnapshotID, suffix)
	if !rr.OK {
		t.Fatalf("a restore of a backup with known missing files must still succeed: %+v", rr)
	}
	if len(rr.Warnings) == 0 {
		t.Fatalf("expected a warning about files already missing at backup time")
	}
	f.assertState(f.db, before, "restored")
}

func TestBackupDefersForFilestoreCleanup(t *testing.T) {
	f := newFixture(t)
	f.seed(1)
	wait := func(o *BackupOptions) { o.WaitForCleanup = true; o.CleanupWindow = 10 * time.Minute }

	f.exec(f.db, "UPDATE ir_cron SET nextcall = (now() at time zone 'utc') + interval '3 minutes'")
	res := f.backup("soon", wait)
	if !res.OK || !res.Deferred {
		t.Fatalf("a cleanup due in 3 minutes must defer the backup: %+v", res)
	}
	if snaps, err := f.env.Restic.Snapshots(context.Background(), layout.Host(itNamespace, itDeployment)); err == nil && len(snaps) > 0 {
		t.Fatal("a deferred backup must not write a snapshot")
	}

	// Overdue by more than an hour: the cron is not running, do not wait.
	f.exec(f.db, "UPDATE ir_cron SET nextcall = (now() at time zone 'utc') - interval '5 hours'")
	if res := f.backup("overdue", wait); !res.OK || res.Deferred {
		t.Fatalf("an overdue cleanup must not defer the backup: %+v", res)
	}

	// Running right now: the job row is locked by Odoo's cron worker.
	f.exec(f.db, "UPDATE ir_cron SET nextcall = (now() at time zone 'utc') + interval '1 day'")
	lock := f.conn(f.db)
	tx, err := lock.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), "SELECT 1 FROM ir_cron WHERE id = 1 FOR NO KEY UPDATE"); err != nil {
		t.Fatal(err)
	}
	if res := f.backup("running", wait); !res.OK || !res.Deferred {
		t.Fatalf("a running cleanup must defer the backup: %+v", res)
	}
	_ = tx.Rollback(context.Background())

	// Without the wait it never defers.
	f.exec(f.db, "UPDATE ir_cron SET nextcall = (now() at time zone 'utc') + interval '1 minute'")
	if res := f.backup("nowait"); !res.OK || res.Deferred {
		t.Fatalf("without --wait-for-cleanup: %+v", res)
	}
}

// Writes during the backup never make the dump disagree with the file list
// or row counts: everything comes from one database snapshot.
func TestBackupIsConsistentUnderWrites(t *testing.T) {
	f := newFixture(t)
	f.seed(20)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := pg.Connect(ctx, f.cfg, f.db)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(context.Background()) }()
		for i := 0; ctx.Err() == nil; i++ {
			_, _ = conn.Exec(ctx, "INSERT INTO extra.audit (payload) VALUES ($1)", fmt.Sprintf(`{"w": %d}`, i))
			_, _ = conn.Exec(ctx, "UPDATE res_partner SET amount = amount + 1 WHERE id = $1", 1+i%1500)
		}
	}()
	res := f.backup("busy")
	cancel()
	wg.Wait()
	if !res.OK {
		t.Fatalf("backup: %+v", res)
	}
	// The restore's checks compare exact row counts with the manifest.
	if rr := f.restore(res.SnapshotID, f.nextSuffix()); !rr.OK {
		t.Fatalf("restore of a backup taken under writes: %+v", rr)
	}
}

// A file Odoo deletes after the backup listed it (and that the previous
// backup does not hold) is reported, not silently dropped.
func TestBackupReportsFileLostDuringUpload(t *testing.T) {
	f := newFixture(t)
	f.seed(3)
	root := t.TempDir()
	if err := linkTree(f.filestore(f.db), filepath.Join(root, layout.FilestoreDir)); err != nil {
		t.Fatal(err)
	}
	var victim string
	for name := range files(t, filepath.Join(root, layout.FilestoreDir)) {
		victim = name
		break
	}
	f.env.Fault = func(point string) error {
		if point == FaultBackupDumped {
			return os.Remove(filepath.Join(root, layout.FilestoreDir, victim))
		}
		return nil
	}
	res := Backup(context.Background(), f.env, BackupOptions{
		Root: root, Namespace: itNamespace, Deployment: itDeployment, Backup: "lost", Kind: "manual", Version: "test",
	})
	if !res.OK || res.MissingFiles != 1 || !contains(res.MissingNames, victim) {
		t.Fatalf("expected the deleted file to be reported missing: %+v", res)
	}
}

func TestRestoreIntoInstanceWithoutDatabase(t *testing.T) {
	f := newFixture(t)
	f.seed(5)
	before := f.state(f.db)
	res := f.mustBackup("first")

	conn := f.conn(f.srv.maint)
	if err := pg.Drop(context.Background(), conn, f.db); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.filestore(f.db)); err != nil {
		t.Fatal(err)
	}
	suffix := f.nextSuffix()
	rr := f.restore(res.SnapshotID, suffix)
	if !rr.OK || rr.PreviousKept {
		t.Fatalf("restore into an empty instance: %+v", rr)
	}
	f.assertState(f.db, before, "restored")
	if got := f.databases(); strings.Join(got, ",") != f.db {
		t.Fatalf("databases: %v", got)
	}
}

func TestRestoreRefusesAnotherOdooSeries(t *testing.T) {
	f := newFixture(t)
	f.seed(2)
	res := f.mustBackup("first")
	f.exec(f.db, "UPDATE ir_module_module SET latest_version = '19.0.1.3' WHERE name = 'base'")
	live := f.state(f.db)
	rr := f.restore(res.SnapshotID, f.nextSuffix())
	if rr.OK || rr.LiveChanged || rr.Step != result.StepCheck || !strings.Contains(rr.Error, "18.0") {
		t.Fatalf("expected the restore to refuse an 18.0 backup on a 19.0 database: %+v", rr)
	}
	f.assertState(f.db, live, "after the refused restore")
	if got := f.databases(); strings.Join(got, ",") != f.db {
		t.Fatalf("databases: %v", got)
	}
}

func TestRestoreUnknownSnapshot(t *testing.T) {
	f := newFixture(t)
	f.seed(1)
	f.mustBackup("first")
	live := f.state(f.db)
	rr := f.restore("0123456789abcdef", f.nextSuffix())
	if rr.OK || rr.LiveChanged || rr.Step != result.StepFetch {
		t.Fatalf("restore of an unknown snapshot: %+v", rr)
	}
	f.assertState(f.db, live, "after the failed restore")
}

// The checks after loading catch a restored copy that does not match the
// backup, and the live data stays untouched.
func TestRestoreChecksCatchDamage(t *testing.T) {
	cases := map[string]struct {
		point  string
		damage func(f *fixture, suffix string)
		want   string
	}{
		"a missing row": {
			point: FaultRestoreFilestore,
			damage: func(f *fixture, suffix string) {
				f.exec(layout.RestoreName(f.db, suffix), "DELETE FROM extra.audit WHERE id = 7")
			},
			want: "extra.audit has 299 rows instead of 300",
		},
		"a missing file": {
			point: FaultRestoreFilestore,
			damage: func(f *fixture, suffix string) {
				dir := f.filestore(layout.RestoreName(f.db, suffix))
				for name := range files(f.t, dir) {
					_ = os.Remove(filepath.Join(dir, name))
					return
				}
			},
			want: "missing after the restore",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seed(5)
			res := f.mustBackup("first")
			f.mutate()
			live := f.state(f.db)
			suffix := f.nextSuffix()
			f.env.Fault = func(point string) error {
				if point == tc.point {
					tc.damage(f, suffix)
				}
				return nil
			}
			rr := f.restore(res.SnapshotID, suffix)
			if rr.OK || rr.LiveChanged || rr.Step != result.StepCheck || !strings.Contains(rr.Error, tc.want) {
				t.Fatalf("expected the check to fail with %q: %+v", tc.want, rr)
			}
			f.assertState(f.db, live, "live data after the failed check")
			if got := f.databases(); strings.Join(got, ",") != f.db {
				t.Fatalf("the half-restored copy was not removed: %v", got)
			}
			if _, err := os.Stat(f.filestore(layout.RestoreName(f.db, suffix))); !os.IsNotExist(err) {
				t.Fatalf("the half-restored filestore was not removed: %v", err)
			}
		})
	}
}

// A sampled file whose content does not match its name was damaged before
// the backup; the restore warns but succeeds.
func TestRestoreWarnsAboutDamagedFiles(t *testing.T) {
	f := newFixture(t)
	f.seed(3)
	dir := f.filestore(f.db)
	for name := range files(t, dir) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("bit rot"), 0o644); err != nil {
			t.Fatal(err)
		}
		break
	}
	res := f.mustBackup("first")
	rr := f.restore(res.SnapshotID, f.nextSuffix())
	if !rr.OK || len(rr.Warnings) == 0 || !strings.Contains(strings.Join(rr.Warnings, " "), "do not match their name") {
		t.Fatalf("expected a warning about the damaged file: %+v", rr)
	}
}

// Every point of a restore can fail, gracefully or as a crash, and the
// restore is then either cleanly abandoned (before the swap: live data
// untouched) or finished by running it again (from the swap on).
func TestRestoreResumesFromEveryPoint(t *testing.T) {
	points := []struct {
		point    string
		swapping bool
	}{
		{FaultRestoreLoaded, false},
		{FaultRestoreFilestore, false},
		{FaultRestoreChecked, false},
		{FaultSwapStarted, true},
		{FaultSwapDatabases, true},
		{FaultSwapLiveAside, true},
		{FaultSwapFilestore, true},
	}
	for _, p := range points {
		for _, crash := range []bool{false, true} {
			name := p.point + "/error"
			if crash {
				name = p.point + "/crash"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				f.seed(15)
				backupState := f.state(f.db)
				res := f.mustBackup("first")
				f.mutate()
				live := f.state(f.db)
				suffix := f.nextSuffix()

				f.env.Fault = func(point string) error {
					if point != p.point {
						return nil
					}
					if crash {
						panic("simulated crash at " + point)
					}
					return errors.New("simulated failure at " + point)
				}
				var rr result.Result
				crashed := func() (crashed bool) {
					defer func() {
						if recover() != nil {
							crashed = true
						}
					}()
					rr = f.restore(res.SnapshotID, suffix)
					return false
				}()
				if crashed != crash {
					t.Fatalf("crashed=%v, want %v (result %+v)", crashed, crash, rr)
				}
				if !crash {
					if rr.OK || rr.LiveChanged != p.swapping {
						t.Fatalf("failed attempt: %+v (liveChanged should be %v)", rr, p.swapping)
					}
				}
				if !p.swapping {
					f.assertState(f.db, live, "live data after a failure before the swap")
				}
				// The cleanup refuses while a swap is unfinished.
				if p.swapping {
					if cr := f.cleanup(suffix); cr.OK {
						t.Fatalf("cleanup must refuse during an unfinished swap")
					}
				}

				f.env.Fault = nil
				rr = f.restore(res.SnapshotID, suffix)
				if !rr.OK || !rr.LiveChanged || !rr.PreviousKept {
					t.Fatalf("retried restore: %+v", rr)
				}
				f.assertState(f.db, backupState, "after the retried restore")
				prev := layout.PrevName(f.db, suffix)
				f.assertState(prev, live, "the replaced data after the retried restore")
				if c := f.comment(f.db); c != itOwnerTag {
					t.Fatalf("owner tag after the retried restore: %q", c)
				}
				if got := f.databases(); strings.Join(got, ",") != strings.Join(sorted(f.db, prev), ",") {
					t.Fatalf("databases: %v", got)
				}
				// Running it once more after it finished changes nothing.
				if rr := f.restore(res.SnapshotID, suffix); !rr.OK {
					t.Fatalf("a finished restore run again: %+v", rr)
				}
				f.assertState(f.db, backupState, "after running the finished restore again")
				f.assertState(prev, live, "the replaced data after running the finished restore again")
			})
		}
	}
}

func TestUndo(t *testing.T) {
	f := newFixture(t)
	f.seed(8)
	backupState := f.state(f.db)
	res := f.mustBackup("first")
	f.mutate()
	live := f.state(f.db)

	s1 := f.nextSuffix()
	if rr := f.restore(res.SnapshotID, s1); !rr.OK {
		t.Fatalf("restore: %+v", rr)
	}
	s2 := f.nextSuffix()
	ur := f.undo(s1, s2)
	if !ur.OK || !ur.PreviousKept {
		t.Fatalf("undo: %+v", ur)
	}
	if ur.OdooVersion != "18.0" || len(ur.MissingModules) != 0 {
		t.Fatalf("undo result: %+v", ur)
	}
	f.assertState(f.db, live, "after the undo")
	f.assertState(layout.PrevName(f.db, s2), backupState, "the data the undo replaced")
	if c := f.comment(f.db); c != itOwnerTag {
		t.Fatalf("owner tag after the undo: %q", c)
	}

	// The first restore's cleanup finds nothing of its own left; the undo's
	// cleanup deletes the restored data. Neither touches live data.
	if cr := f.cleanup(s1); !cr.OK {
		t.Fatalf("cleanup of the undone restore: %+v", cr)
	}
	if cr := f.cleanup(s2); !cr.OK {
		t.Fatalf("cleanup of the undo: %+v", cr)
	}
	f.assertState(f.db, live, "after both cleanups")
	if got := f.databases(); strings.Join(got, ",") != f.db {
		t.Fatalf("databases: %v", got)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.dataRoot, "filestore")); len(entries) != 1 {
		t.Fatalf("filestore folders left: %v", entries)
	}
}

func TestUndoResumesFromEveryPoint(t *testing.T) {
	for _, point := range []string{FaultSwapStarted, FaultSwapDatabases, FaultSwapLiveAside, FaultSwapFilestore} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t)
			f.seed(5)
			backupState := f.state(f.db)
			res := f.mustBackup("first")
			f.mutate()
			live := f.state(f.db)
			s1 := f.nextSuffix()
			if rr := f.restore(res.SnapshotID, s1); !rr.OK {
				t.Fatalf("restore: %+v", rr)
			}
			s2 := f.nextSuffix()
			f.env.Fault = func(p string) error {
				if p == point {
					return errors.New("simulated failure")
				}
				return nil
			}
			if ur := f.undo(s1, s2); ur.OK || !ur.LiveChanged {
				t.Fatalf("failed undo: %+v", ur)
			}
			f.env.Fault = nil
			if ur := f.undo(s1, s2); !ur.OK {
				t.Fatalf("retried undo: %+v", ur)
			}
			f.assertState(f.db, live, "after the retried undo")
			f.assertState(layout.PrevName(f.db, s2), backupState, "the data the undo replaced")
		})
	}
}

func TestUndoRefusesWhenPreviousDataIsGone(t *testing.T) {
	f := newFixture(t)
	f.seed(2)
	res := f.mustBackup("first")
	s1 := f.nextSuffix()
	if rr := f.restore(res.SnapshotID, s1); !rr.OK {
		t.Fatalf("restore: %+v", rr)
	}
	if cr := f.cleanup(s1); !cr.OK {
		t.Fatalf("cleanup: %+v", cr)
	}
	live := f.state(f.db)
	ur := f.undo(s1, f.nextSuffix())
	if ur.OK || ur.LiveChanged || !strings.Contains(ur.Error, "no longer exists") {
		t.Fatalf("undo after the cleanup: %+v", ur)
	}
	f.assertState(f.db, live, "after the refused undo")
}

func TestCleanupResumes(t *testing.T) {
	for _, point := range []string{FaultCleanupDatabase, FaultCleanupFilestore} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t)
			f.seed(3)
			res := f.mustBackup("first")
			s := f.nextSuffix()
			if rr := f.restore(res.SnapshotID, s); !rr.OK {
				t.Fatalf("restore: %+v", rr)
			}
			live := f.state(f.db)
			f.env.Fault = func(p string) error {
				if p == point {
					return errors.New("simulated failure")
				}
				return nil
			}
			if cr := f.cleanup(s); cr.OK {
				t.Fatal("expected the cleanup to fail")
			}
			f.env.Fault = nil
			if cr := f.cleanup(s); !cr.OK {
				t.Fatalf("retried cleanup: %+v", cr)
			}
			f.assertState(f.db, live, "after the cleanup")
			if got := f.databases(); strings.Join(got, ",") != f.db {
				t.Fatalf("databases: %v", got)
			}
		})
	}
}

// The cleanup only ever deletes names built from its own suffix.
func TestCleanupNeverTouchesOtherData(t *testing.T) {
	f := newFixture(t)
	f.seed(2)
	conn := f.conn(f.srv.maint)
	other := f.db + "_other"
	for _, n := range []string{other, layout.PrevName(f.db, "20260101T000000")} {
		if err := pg.CreateLike(context.Background(), conn, n, pg.DatabaseInfo{}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(f.filestore(n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	live := f.state(f.db)
	if cr := f.cleanup("20260202T000000"); !cr.OK {
		t.Fatalf("cleanup: %+v", cr)
	}
	if cr := f.cleanup("../../x"); cr.OK {
		t.Fatal("a cleanup with an invalid suffix must refuse")
	}
	f.assertState(f.db, live, "live after the cleanup")
	want := sorted(f.db, other, layout.PrevName(f.db, "20260101T000000"))
	if got := f.databases(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("databases: %v, want %v", got, want)
	}
}

func TestRestoreWrongRepositoryPassword(t *testing.T) {
	f := newFixture(t)
	f.seed(1)
	res := f.mustBackup("first")
	env := *f.env
	r := *f.env.Restic
	r.Env = append(append([]string(nil), r.Env...), "RESTIC_PASSWORD=wrong")
	env.Restic = &r
	live := f.state(f.db)
	rr := Restore(context.Background(), &env, RestoreOptions{
		Mode: ModeRestore, Suffix: f.nextSuffix(), Snapshot: res.SnapshotID, DataRoot: f.dataRoot,
	})
	if rr.OK || rr.LiveChanged || !strings.Contains(strings.ToLower(rr.Error), "password") {
		t.Fatalf("restore with the wrong repository password: %+v", rr)
	}
	f.assertState(f.db, live, "after the failed restore")
}

func TestBackupAfterRestoreKeepsWorking(t *testing.T) {
	f := newFixture(t)
	f.seed(6)
	r1 := f.mustBackup("first")
	f.mutate()
	r2 := f.mustBackup("second")
	if r2.AddedBytes >= r1.AddedBytes {
		t.Fatalf("the second backup should be deduplicated: first added %d, second %d", r1.AddedBytes, r2.AddedBytes)
	}
	afterMutate := f.state(f.db)
	if rr := f.restore(r1.SnapshotID, f.nextSuffix()); !rr.OK {
		t.Fatalf("restore: %+v", rr)
	}
	// Back to the newer backup.
	if rr := f.restore(r2.SnapshotID, f.nextSuffix()); !rr.OK {
		t.Fatalf("second restore: %+v", rr)
	}
	f.assertState(f.db, afterMutate, "after restoring the newer backup")
	if r3 := f.mustBackup("third"); r3.MissingFiles != 0 {
		t.Fatalf("backup after restores: %+v", r3)
	}
}
