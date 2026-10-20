package pg

// Tests of the parts of package pg that need no database.

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/odooconf"
)

func TestDSN(t *testing.T) {
	c := odooconf.Config{Host: "pg.db.svc", Port: 6432, User: "odoo", Password: "p@ss:/word?#&=%", SSLMode: "require"}
	dsn := DSN(c, "my db/é")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgres" || u.Host != "pg.db.svc:6432" || u.User.Username() != "odoo" {
		t.Fatalf("DSN = %s", dsn)
	}
	if pw, _ := u.User.Password(); pw != c.Password {
		t.Fatalf("password = %q", pw)
	}
	if u.Path != "/my db/é" {
		t.Fatalf("path = %q", u.Path)
	}
	q := u.Query()
	if q.Get("sslmode") != "require" || q.Get("connect_timeout") != "10" || q.Get("application_name") != "odoo-operator-backup" {
		t.Fatalf("query = %v", q)
	}
}

func TestDSNDefaultsAndIPv6(t *testing.T) {
	u, err := url.Parse(DSN(odooconf.Config{Port: 5432, SSLMode: "prefer"}, "x"))
	if err != nil || u.Host != "localhost:5432" {
		t.Fatalf("default host: %v %v", u, err)
	}
	u, err = url.Parse(DSN(odooconf.Config{Host: "::1", Port: 5432, SSLMode: "disable"}, "x"))
	if err != nil || u.Host != "[::1]:5432" || u.Hostname() != "::1" {
		t.Fatalf("ipv6 host: %v %v", u, err)
	}
}

func TestIdent(t *testing.T) {
	cases := map[string]string{
		"prod":                "\"prod\"",
		"Prod":                "\"Prod\"",
		`we"ird`:              `"we""ird"`,
		"with space":          `"with space"`,
		"prod_restore_2025":   `"prod_restore_2025"`,
		"a.b":                 `"a.b"`,
		`"; DROP DATABASE x;`: `"""; DROP DATABASE x;"`,
	}
	for in, want := range cases {
		if got := Ident(in); got != want {
			t.Errorf("Ident(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLiteral(t *testing.T) {
	cases := map[string]string{
		"":                  "''",
		"UTF8":              "'UTF8'",
		"en_US.UTF-8":       "'en_US.UTF-8'",
		"it's":              "'it''s'",
		"''":                "''''''",
		"x'; DROP x; --":    "'x''; DROP x; --'",
		`back\slash`:        `'back\slash'`, // standard_conforming_strings (default on)
		"é中":                "'é中'",
		"odoo-operator:a/b": "'odoo-operator:a/b'",
	}
	for in, want := range cases {
		if got := Literal(in); got != want {
			t.Errorf("Literal(%q) = %s, want %s", in, got, want)
		}
	}
	// Whatever the input, the literal has an even number of quotes inside
	// its delimiters and none of them is unpaired.
	for _, in := range []string{"'", "a'b'c", strings.Repeat("'", 7)} {
		lit := Literal(in)
		inner := lit[1 : len(lit)-1]
		if strings.ReplaceAll(inner, "''", "") != strings.ReplaceAll(in, "'", "") {
			t.Errorf("Literal(%q) = %s leaves an unpaired quote", in, lit)
		}
	}
}

func TestMajorAndTool(t *testing.T) {
	cases := map[int]int{160004: 16, 170000: 17, 180001: 18, 90624: 9, 100023: 10}
	for in, want := range cases {
		if got := Major(in); got != want {
			t.Errorf("Major(%d) = %d", in, got)
		}
	}
	if Tool("", "pg_dump") != "pg_dump" {
		t.Error("Tool without dir")
	}
	if Tool("/usr/lib/postgresql/16/bin", "psql") != "/usr/lib/postgresql/16/bin/psql" {
		t.Error("Tool with dir")
	}
}

func installClients(t *testing.T, majors ...int) string {
	t.Helper()
	root := t.TempDir()
	for _, m := range majors {
		dir := filepath.Join(root, "pg"+strconv.Itoa(m), "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pg_dump"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "pg%d", "bin")
}

func TestClientsPick(t *testing.T) {
	tmpl := installClients(t, 13, 15, 16, 18)
	c := Clients{BinDirTemplate: tmpl, Majors: []int{18, 17, 16, 15, 14, 13}} // unsorted on purpose
	cases := []struct {
		server int
		want   int // 0 = error
	}{
		{12, 13},
		{13, 13},
		{14, 15}, // 14 is not installed: the next newer client
		{15, 15},
		{16, 16},
		{17, 18}, // 17 is listed but not installed
		{18, 18},
		{19, 0}, // newer than every client
	}
	for _, cs := range cases {
		dir, err := c.Pick(cs.server)
		if cs.want == 0 {
			if err == nil {
				t.Errorf("Pick(%d) = %q, want an error", cs.server, dir)
			} else if !strings.Contains(err.Error(), "[13 15 16 18]") {
				t.Errorf("Pick(%d) error %q does not list the installed clients", cs.server, err)
			}
			continue
		}
		if err != nil || dir != strings.Replace(tmpl, "%d", strconv.Itoa(cs.want), 1) {
			t.Errorf("Pick(%d) = %q, %v; want client %d", cs.server, dir, err, cs.want)
		}
	}
	if c.Majors[0] != 18 {
		t.Error("Pick sorted the caller's slice")
	}
}

func TestClientsPickNothingInstalled(t *testing.T) {
	c := Clients{BinDirTemplate: filepath.Join(t.TempDir(), "pg%d"), Majors: []int{16}}
	if _, err := c.Pick(16); err == nil || !strings.Contains(err.Error(), "[]") {
		t.Fatalf("Pick = %v", err)
	}
	// A folder without pg_dump does not count.
	dir := filepath.Join(t.TempDir(), "pg16")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c = Clients{BinDirTemplate: strings.Replace(dir, "16", "%d", 1), Majors: []int{16}}
	if _, err := c.Pick(16); err == nil {
		t.Fatal("a folder without pg_dump was picked")
	}
}

func TestClientsPickFromPath(t *testing.T) {
	dir, err := Clients{}.Pick(99)
	if dir != "" || err != nil {
		t.Fatalf("Pick without template = %q, %v", dir, err)
	}
}

func TestShouldWait(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	cases := []struct {
		name string
		av   Autovacuum
		wait bool
	}{
		{"no cron", Autovacuum{Now: now, NextCall: now}, false},
		{"running", Autovacuum{Found: true, Running: true, Now: now, NextCall: now.Add(24 * time.Hour)}, true},
		{"running although inactive", Autovacuum{Found: true, Running: true, Active: false, Now: now}, true},
		{"inactive and due", Autovacuum{Found: true, Active: false, Now: now, NextCall: now.Add(time.Minute)}, false},
		{"due in 5 minutes", Autovacuum{Found: true, Active: true, Now: now, NextCall: now.Add(5 * time.Minute)}, true},
		{"due exactly at the window", Autovacuum{Found: true, Active: true, Now: now, NextCall: now.Add(window)}, false},
		{"due after the window", Autovacuum{Found: true, Active: true, Now: now, NextCall: now.Add(11 * time.Minute)}, false},
		{"due now", Autovacuum{Found: true, Active: true, Now: now, NextCall: now}, true},
		{"overdue 30 minutes (about to run)", Autovacuum{Found: true, Active: true, Now: now, NextCall: now.Add(-30 * time.Minute)}, true},
		{"overdue exactly an hour", Autovacuum{Found: true, Active: true, Now: now, NextCall: now.Add(-time.Hour)}, false},
		{"overdue a day (no cron worker)", Autovacuum{Found: true, Active: true, Now: now, NextCall: now.Add(-24 * time.Hour)}, false},
	}
	for _, c := range cases {
		wait, why := c.av.ShouldWait(window)
		if wait != c.wait {
			t.Errorf("%s: ShouldWait = %v (%q)", c.name, wait, why)
		}
		if wait && !strings.Contains(why, "base.autovacuum_job") {
			t.Errorf("%s: reason %q does not name the cron", c.name, why)
		}
		if !wait && why != "" {
			t.Errorf("%s: reason %q without waiting", c.name, why)
		}
	}
	_, why := Autovacuum{Found: true, Active: true, Now: now, NextCall: now.Add(5 * time.Minute)}.ShouldWait(window)
	if !strings.Contains(why, "12:05") {
		t.Errorf("reason %q does not say when", why)
	}
	if wait, _ := (Autovacuum{Found: true, Active: true, Now: now, NextCall: now.Add(time.Minute)}).ShouldWait(0); wait {
		t.Error("a zero window still waits for a cron due in the future")
	}
}

func TestClientsNewest(t *testing.T) {
	tmpl := installClients(t, 13, 16)
	c := Clients{BinDirTemplate: tmpl, Majors: []int{13, 18, 16}}
	dir, err := c.Newest()
	if err != nil || dir != fmt.Sprintf(tmpl, 16) {
		t.Fatalf("Newest = %q, %v; want the 16 client (18 is listed but not installed)", dir, err)
	}
	if dir, err := (Clients{}).Newest(); err != nil || dir != "" {
		t.Fatalf("Newest without a template = %q, %v", dir, err)
	}
	if _, err := (Clients{BinDirTemplate: filepath.Join(t.TempDir(), "pg%d"), Majors: []int{16}}).Newest(); err == nil {
		t.Fatal("Newest with nothing installed: want an error")
	}
}
