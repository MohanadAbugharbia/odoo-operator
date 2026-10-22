package database

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestDSN_EscapesUserAndPassword(t *testing.T) {
	c := ConnInfo{
		Host:          "shared-pg-rw.shared.svc.cluster.local",
		Port:          5432,
		User:          "pal odoo@x",
		Password:      "p@ss/w:rd#1 ü",
		MaintenanceDB: "postgres",
	}
	dsn := DSN(c, c.MaintenanceDB)

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN is not a valid URL: %v\n%s", err, dsn)
	}
	if got := u.User.Username(); got != c.User {
		t.Errorf("user = %q, want %q", got, c.User)
	}
	if got, _ := u.User.Password(); got != c.Password {
		t.Errorf("password = %q, want %q", got, c.Password)
	}
	if u.Hostname() != c.Host || u.Port() != "5432" {
		t.Errorf("host:port = %s:%s", u.Hostname(), u.Port())
	}
	if u.Path != "/postgres" {
		t.Errorf("path = %q", u.Path)
	}
	if strings.Contains(dsn, "p@ss/w:rd#1") {
		t.Errorf("password not escaped: %s", dsn)
	}
	for _, want := range []string{"sslmode=disable", "connect_timeout=10", "application_name=odoo-operator", "default_query_exec_mode=simple_protocol"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN missing %q: %s", want, dsn)
		}
	}
}

func TestDSN_SSLMode(t *testing.T) {
	c := ConnInfo{Host: "db", Port: 5432, User: "u", Password: "p", MaintenanceDB: "postgres"}
	if dsn := DSN(c, "x"); !strings.Contains(dsn, "sslmode=disable") {
		t.Errorf("ssl=false → want sslmode=disable, got %s", dsn)
	}
	c.SSL = true
	if dsn := DSN(c, "x"); !strings.Contains(dsn, "sslmode=require") {
		t.Errorf("ssl=true → want sslmode=require, got %s", dsn)
	}
}

func TestDSN_IPv6Host(t *testing.T) {
	c := ConnInfo{Host: "::1", Port: 5433, User: "u", Password: "p"}
	dsn := DSN(c, "db")
	if !strings.Contains(dsn, "@[::1]:5433/db") {
		t.Errorf("IPv6 host not bracketed: %s", dsn)
	}
}

func TestQuoteIdentifier(t *testing.T) {
	tests := map[string]string{
		`pal_odoo_pr_12`: `"pal_odoo_pr_12"`,
		`weird"name`:     `"weird""name"`,
		`Mixed-Case`:     `"Mixed-Case"`,
	}
	for in, want := range tests {
		if got := quoteIdentifier(in); got != want {
			t.Errorf("quoteIdentifier(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	if got := quoteLiteral("odoo-operator:ns/name"); got != "'odoo-operator:ns/name'" {
		t.Errorf("got %s", got)
	}
	if got := quoteLiteral("it's"); got != "'it''s'" {
		t.Errorf("got %s", got)
	}
}

func TestNewOwnerTag(t *testing.T) {
	if got := NewOwnerTag("ababiel-preview", "ababiel-pr-12"); got != "odoo-operator:ababiel-preview/ababiel-pr-12" {
		t.Errorf("got %q", got)
	}
}

func TestCheckCopy(t *testing.T) {
	s := "20250102T030405"
	for _, name := range []string{"shop_restore_" + s, "shop_drill_" + s} {
		if err := CheckCopy("shop", name); err != nil {
			t.Errorf("CheckCopy(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"shop", "shop_prev_" + s, "shop_restore_", "shop_restore_" + s + "_x", "other_restore_" + s, "shop_restore_latest"} {
		if err := CheckCopy("shop", name); !errors.Is(err, ErrNotCopy) {
			t.Errorf("CheckCopy(%q) = %v, want ErrNotCopy", name, err)
		}
	}
	if err := CheckCopy("shop_restore_"+s, "shop_restore_"+s); err == nil {
		t.Error("the live database is a copy")
	}
}

func TestFakeDropCopy(t *testing.T) {
	f := NewFake()
	tag := NewOwnerTag("ns", "odoo")
	f.Seed("shop", tag)
	f.Seed("shop_restore_20250102T030405", "")
	f.Seed("shop_drill_20250102T030405", tag)
	f.Seed("shop_drill_20250102T030406", "odoo-operator:ns/other")
	ctx := context.Background()
	for _, name := range []string{"shop_restore_20250102T030405", "shop_drill_20250102T030405", "shop_drill_20250102T030407"} {
		if err := f.DropCopy(ctx, ConnInfo{}, "shop", name, tag); err != nil {
			t.Fatalf("DropCopy(%s): %v", name, err)
		}
	}
	if err := f.DropCopy(ctx, ConnInfo{}, "shop", "shop_drill_20250102T030406", tag); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("foreign copy: %v", err)
	}
	if err := f.DropCopy(ctx, ConnInfo{}, "shop", "shop", tag); !errors.Is(err, ErrNotCopy) {
		t.Fatalf("live: %v", err)
	}
	names, _ := f.Databases(ctx, ConnInfo{})
	if strings.Join(names, ",") != "shop,shop_drill_20250102T030406" {
		t.Fatalf("left %v", names)
	}
}
