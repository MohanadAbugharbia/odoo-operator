package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// These exercise the provisioner against a real server: the SQL it emits, the
// COMMENT it reads back and the refusal to drop a database it does not own are
// not observable through the fake. CI starts a postgres service and sets
// TEST_PG_HOST; without it the suite skips, so `make test` still runs offline.
//
//	TEST_PG_HOST=127.0.0.1 TEST_PG_PORT=5432 \
//	TEST_PG_USER=postgres TEST_PG_PASSWORD=postgres go test ./internal/database/
func testConnInfo(t *testing.T) ConnInfo {
	t.Helper()
	host := os.Getenv("TEST_PG_HOST")
	if host == "" {
		t.Skip("TEST_PG_HOST is not set, skipping the PostgreSQL integration tests")
	}
	port := int32(5432)
	if raw := os.Getenv("TEST_PG_PORT"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			t.Fatalf("TEST_PG_PORT=%q is not a number: %v", raw, err)
		}
		port = int32(parsed)
	}
	user := os.Getenv("TEST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	maintenance := os.Getenv("TEST_PG_MAINTENANCE_DB")
	if maintenance == "" {
		maintenance = "postgres"
	}
	return ConnInfo{
		Host:          host,
		Port:          port,
		User:          user,
		Password:      os.Getenv("TEST_PG_PASSWORD"),
		MaintenanceDB: maintenance,
	}
}

// uniqueDBName keeps parallel packages and reruns from colliding.
func uniqueDBName(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("odoo_operator_test_%d_%d", os.Getpid(), time.Now().UnixNano()%1e6)
	if len(name) > 63 {
		t.Fatalf("generated database name %q is too long", name)
	}
	return name
}

// dropDatabase removes a database whatever its comment says, so a failing test
// cannot leave one behind for the next run.
func dropDatabase(ctx context.Context, t *testing.T, c ConnInfo, db string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, DSN(c, c.MaintenanceDB))
	if err != nil {
		t.Logf("cleanup: cannot connect to drop %q: %v", db, err)
		return
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdentifier(db)+" WITH (FORCE)"); err != nil {
		t.Logf("cleanup: cannot drop %q: %v", db, err)
	}
}

func TestPgxDatabaseLifecycle(t *testing.T) {
	conn := testConnInfo(t)
	ctx := context.Background()
	p := NewPgx()
	db := uniqueDBName(t)
	tag := NewOwnerTag("ababiel-preview", "ababiel-pr-12")
	t.Cleanup(func() { dropDatabase(ctx, t, conn, db) })

	exists, err := p.Exists(ctx, conn, db)
	if err != nil {
		t.Fatalf("Exists before create: %v", err)
	}
	if exists {
		t.Fatalf("database %q already exists", db)
	}

	if err := p.Create(ctx, conn, db, tag); err != nil {
		t.Fatalf("Create: %v", err)
	}

	exists, err = p.Exists(ctx, conn, db)
	if err != nil {
		t.Fatalf("Exists after create: %v", err)
	}
	if !exists {
		t.Fatal("database does not exist after Create")
	}

	got, err := p.OwnerTag(ctx, conn, db)
	if err != nil {
		t.Fatalf("OwnerTag: %v", err)
	}
	if got != tag {
		t.Errorf("owner tag = %q, want %q", got, tag)
	}

	// Odoo requires this exact encoding and collation; a database created with
	// the cluster defaults fails at install time, not here.
	assertEncoding(ctx, t, conn, db)

	// Creating it a second time is an error rather than a silent no-op: the
	// reconciler checks Exists first, and a race must not be swallowed.
	if err := p.Create(ctx, conn, db, tag); err == nil {
		t.Error("Create over an existing database should fail")
	}

	if err := p.Drop(ctx, conn, db, tag); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	exists, err = p.Exists(ctx, conn, db)
	if err != nil {
		t.Fatalf("Exists after drop: %v", err)
	}
	if exists {
		t.Error("database still exists after Drop")
	}

	// Dropping again is a no-op, so a retried finalizer converges.
	if err := p.Drop(ctx, conn, db, tag); err != nil {
		t.Errorf("second Drop should be a no-op, got %v", err)
	}
}

func assertEncoding(ctx context.Context, t *testing.T, c ConnInfo, db string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, DSN(c, c.MaintenanceDB))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var encoding, collate, ctype string
	err = conn.QueryRow(ctx,
		`SELECT pg_encoding_to_char(encoding), datcollate, datctype
		   FROM pg_database WHERE datname = $1`, db,
	).Scan(&encoding, &collate, &ctype)
	if err != nil {
		t.Fatalf("read encoding: %v", err)
	}
	if encoding != "UTF8" {
		t.Errorf("encoding = %q, want UTF8", encoding)
	}
	if collate != "C" || ctype != "C" {
		t.Errorf("collate/ctype = %q/%q, want C/C", collate, ctype)
	}
}

// The safety rule the whole ownership scheme exists for: a database the
// operator did not create is never dropped, however the CR is configured.
func TestPgxDropRefusesForeignDatabase(t *testing.T) {
	conn := testConnInfo(t)
	ctx := context.Background()
	p := NewPgx()
	db := uniqueDBName(t)
	t.Cleanup(func() { dropDatabase(ctx, t, conn, db) })

	ours := NewOwnerTag("ababiel-preview", "ababiel-pr-12")
	theirs := NewOwnerTag("production", "ababiel")

	if err := p.Create(ctx, conn, db, theirs); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := p.Drop(ctx, conn, db, ours)
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("Drop with a foreign tag: err = %v, want ErrNotOwned", err)
	}
	if !strings.Contains(err.Error(), string(theirs)) {
		t.Errorf("error %q does not report the comment it found", err)
	}

	exists, err := p.Exists(ctx, conn, db)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("the refused Drop deleted the database anyway")
	}
}

// A database that predates the operator has no comment at all. Adopting it
// must not drop it either, and Tag must be able to claim one later.
func TestPgxUntaggedAndAdoptedDatabases(t *testing.T) {
	conn := testConnInfo(t)
	ctx := context.Background()
	p := NewPgx()
	db := uniqueDBName(t)
	tag := NewOwnerTag("ababiel-preview", "ababiel-pr-12")
	t.Cleanup(func() { dropDatabase(ctx, t, conn, db) })

	// Create it outside the operator, exactly as a human would.
	raw, err := pgx.Connect(ctx, DSN(conn, conn.MaintenanceDB))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := raw.Exec(ctx, "CREATE DATABASE "+quoteIdentifier(db)); err != nil {
		_ = raw.Close(ctx)
		t.Fatalf("create the pre-existing database: %v", err)
	}
	_ = raw.Close(ctx)

	got, err := p.OwnerTag(ctx, conn, db)
	if err != nil {
		t.Fatalf("OwnerTag on an untagged database: %v", err)
	}
	if got != "" {
		t.Errorf("owner tag = %q, want empty for an untagged database", got)
	}

	if err := p.Drop(ctx, conn, db, tag); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("Drop of an untagged database: err = %v, want ErrNotOwned", err)
	}

	// Tag claims it (the crash-recovery path: created, then tagged).
	if err := p.Tag(ctx, conn, db, tag); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	got, err = p.OwnerTag(ctx, conn, db)
	if err != nil {
		t.Fatalf("OwnerTag after Tag: %v", err)
	}
	if got != tag {
		t.Errorf("owner tag = %q, want %q", got, tag)
	}
	if err := p.Drop(ctx, conn, db, tag); err != nil {
		t.Errorf("Drop after Tag: %v", err)
	}
}

func TestPgxOwnerTagOfMissingDatabase(t *testing.T) {
	conn := testConnInfo(t)
	ctx := context.Background()

	_, err := NewPgx().OwnerTag(ctx, conn, uniqueDBName(t))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("OwnerTag of a missing database: err = %v, want ErrNotFound", err)
	}
}

// A name that needs quoting must survive every statement the provisioner
// builds by hand, and an apostrophe must not break out of the COMMENT literal.
func TestPgxQuotesAwkwardNames(t *testing.T) {
	conn := testConnInfo(t)
	ctx := context.Background()
	p := NewPgx()
	db := uniqueDBName(t) + "-Mixed.Case"
	tag := OwnerTag("odoo-operator:it's/quoted")
	t.Cleanup(func() { dropDatabase(ctx, t, conn, db) })

	if err := p.Create(ctx, conn, db, tag); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := p.OwnerTag(ctx, conn, db)
	if err != nil {
		t.Fatalf("OwnerTag: %v", err)
	}
	if got != tag {
		t.Errorf("owner tag = %q, want %q", got, tag)
	}
	if err := p.Drop(ctx, conn, db, tag); err != nil {
		t.Errorf("Drop: %v", err)
	}
}

// Every entry point has to surface a connection failure rather than report a
// missing database, or the finalizer would drop its claim on a network blip.
func TestPgxUnreachableServer(t *testing.T) {
	if os.Getenv("TEST_PG_HOST") == "" {
		t.Skip("TEST_PG_HOST is not set, skipping the PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Port 1 is reserved and refuses connections immediately.
	unreachable := ConnInfo{Host: "127.0.0.1", Port: 1, User: "odoo", MaintenanceDB: "postgres"}
	p := NewPgx()

	if _, err := p.Exists(ctx, unreachable, "whatever"); err == nil {
		t.Error("Exists should fail against an unreachable server")
	}
	if err := p.Create(ctx, unreachable, "whatever", "tag"); err == nil {
		t.Error("Create should fail against an unreachable server")
	}
	if err := p.Tag(ctx, unreachable, "whatever", "tag"); err == nil {
		t.Error("Tag should fail against an unreachable server")
	}
	if _, err := p.OwnerTag(ctx, unreachable, "whatever"); err == nil {
		t.Error("OwnerTag should fail against an unreachable server")
	}
	err := p.Drop(ctx, unreachable, "whatever", "tag")
	if err == nil {
		t.Error("Drop should fail against an unreachable server")
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotOwned) {
		t.Errorf("a connection failure must not look like a missing or foreign database: %v", err)
	}
}

// A restore's and a drill's working copies carry no comment: DropCopy drops
// them by name, ending a session a killed pod left, and refuses every other
// name and a copy another owner tagged.
func TestPgxDropCopy(t *testing.T) {
	conn := testConnInfo(t)
	ctx := context.Background()
	p := NewPgx()
	live := uniqueDBName(t)
	suffix := "20250102T030405"
	restoreCopy := live + "_restore_" + suffix
	drillCopy := live + "_drill_" + suffix
	foreign := live + "_restore_20250102T030406"
	prev := live + "_prev_" + suffix
	tag := NewOwnerTag("shop", "odoo")
	admin, err := pgx.Connect(ctx, DSN(conn, conn.MaintenanceDB))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	for _, db := range []string{live, restoreCopy, drillCopy, foreign, prev} {
		t.Cleanup(func() { dropDatabase(ctx, t, conn, db) })
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoteIdentifier(db)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, "COMMENT ON DATABASE "+quoteIdentifier(foreign)+" IS 'odoo-operator:other/odoo'"); err != nil {
		t.Fatal(err)
	}
	held, err := pgx.Connect(ctx, DSN(conn, restoreCopy))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close(ctx) }()

	names, err := p.Databases(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{live, restoreCopy, drillCopy} {
		if !contains(names, want) {
			t.Fatalf("Databases = %v, missing %s", names, want)
		}
	}
	for _, name := range []string{live, prev, "postgres", live + "_restore_latest"} {
		if err := p.DropCopy(ctx, conn, live, name, tag); !errors.Is(err, ErrNotCopy) {
			t.Fatalf("DropCopy(%s) = %v, want ErrNotCopy", name, err)
		}
	}
	if err := p.DropCopy(ctx, conn, live, foreign, tag); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("DropCopy of a copy another owner tagged = %v, want ErrNotOwned", err)
	}
	for _, name := range []string{restoreCopy, drillCopy} {
		if err := p.DropCopy(ctx, conn, live, name, tag); err != nil {
			t.Fatalf("DropCopy(%s): %v", name, err)
		}
		if err := p.DropCopy(ctx, conn, live, name, tag); err != nil {
			t.Fatalf("second DropCopy(%s) is not a no-op: %v", name, err)
		}
	}
	names, err = p.Databases(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range []string{live, prev, foreign} {
		if !contains(names, db) {
			t.Fatalf("%s was dropped", db)
		}
	}
	if contains(names, restoreCopy) || contains(names, drillCopy) {
		t.Fatalf("copies left: %v", names)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestPgxInstalledModules(t *testing.T) {
	conn := testConnInfo(t)
	ctx := context.Background()
	p := NewPgx()
	db := uniqueDBName(t)
	admin, err := pgx.Connect(ctx, DSN(conn, conn.MaintenanceDB))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	t.Cleanup(func() { dropDatabase(ctx, t, conn, db) })
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoteIdentifier(db)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.InstalledModules(ctx, conn, db); err == nil {
		t.Fatal("InstalledModules of a database without Odoo's tables succeeded")
	}
	odoo, err := pgx.Connect(ctx, DSN(conn, db))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = odoo.Close(ctx) }()
	if _, err := odoo.Exec(ctx, `CREATE TABLE ir_module_module (id serial, name varchar, state varchar);
		INSERT INTO ir_module_module (name, state) VALUES ('web', 'installed'), ('base', 'installed'),
		('sale', 'uninstalled'), ('stock', 'to install')`); err != nil {
		t.Fatal(err)
	}
	got, err := p.InstalledModules(ctx, conn, db)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "base,web" {
		t.Fatalf("InstalledModules = %v, want base and web", got)
	}
}
