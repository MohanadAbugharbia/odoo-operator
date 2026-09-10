package database

import (
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
