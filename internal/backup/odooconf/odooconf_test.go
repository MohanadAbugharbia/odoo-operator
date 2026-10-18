package odooconf

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// As the operator renders it.
const rendered = `[options]
addons_path = /mnt/extra-addons
admin_passwd = secret
data_dir = /var/lib/odoo
db_host = postgresql.db.svc
db_port = 5433
db_user = odoo
db_password = p@ss=word;#not-a-comment
db_name = prod
db_sslmode = require
dbfilter = ^prod$
list_db = False
proxy_mode = True
`

func TestParseRendered(t *testing.T) {
	cfg, err := Parse(strings.NewReader(rendered))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Host: "postgresql.db.svc", Port: 5433, User: "odoo", Password: "p@ss=word;#not-a-comment",
		Name: "prod", SSLMode: "require", DataDir: "/var/lib/odoo",
	}
	if cfg != want {
		t.Fatalf("Parse = %+v\nwant %+v", cfg, want)
	}
}

func TestParseDefaultsAndFalse(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`[options]
db_host = False
db_port = False
db_user = False
db_password = False
db_sslmode = False
data_dir = False
db_name = prod
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Port: 5432, SSLMode: "prefer", Name: "prod"}
	if cfg != want {
		t.Fatalf("Parse = %+v, want %+v", cfg, want)
	}
	cfg, err = Parse(strings.NewReader("[options]\ndb_name=only\n"))
	if err != nil || cfg != (Config{Port: 5432, SSLMode: "prefer", Name: "only"}) {
		t.Fatalf("missing keys: %+v, %v", cfg, err)
	}
}

func TestParseIgnoresCommentsAndOtherSections(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
# db_name = commented
; db_host = commented
db_name = before-any-section

[queue_job]
db_name = other
db_host = other-host

[options]
  db_host   =   pg
not a key value line
db_name = prod
[another]
db_name = again
db_port = notanumber
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "prod" || cfg.Host != "pg" || cfg.Port != 5432 {
		t.Fatalf("Parse = %+v", cfg)
	}
}

func TestParseLastValueWins(t *testing.T) {
	cfg, err := Parse(strings.NewReader("[options]\ndb_name = a\ndb_name = b\n"))
	if err != nil || cfg.Name != "b" {
		t.Fatalf("Parse = %+v, %v", cfg, err)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"no db_name":       "[options]\ndb_host = pg\n",
		"db_name False":    "[options]\ndb_name = False\n",
		"empty db_name":    "[options]\ndb_name =\n",
		"no options":       "[other]\ndb_name = prod\n",
		"several dbs":      "[options]\ndb_name = a,b\n",
		"port not number":  "[options]\ndb_name = prod\ndb_port = abc\n",
		"empty input":      "",
		"line too long":    "[options]\ndb_name = prod\nx = " + strings.Repeat("y", 70*1024) + "\n",
		"port with spaces": "[options]\ndb_name = prod\ndb_port = 54 32\n",
	}
	for name, in := range cases {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: Parse succeeded", name)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "odoo.conf")
	if err := os.WriteFile(path, []byte(rendered), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Name != "prod" {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
	if _, err := Load(filepath.Join(dir, "missing.conf")); !os.IsNotExist(err) {
		t.Fatalf("Load(missing) = %v", err)
	}
}

func TestEnv(t *testing.T) {
	full := Config{Host: "pg", Port: 6432, User: "odoo", Password: "pw", Name: "prod", SSLMode: "require"}
	want := []string{
		"PGPORT=6432", "PGDATABASE=other", "PGSSLMODE=require", "PGAPPNAME=odoo-operator-backup",
		"PGHOST=pg", "PGUSER=odoo", "PGPASSWORD=pw",
	}
	if got := full.Env("other"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Env = %q\nwant %q", got, want)
	}
	minimal := Config{Port: 5432, SSLMode: "prefer", Name: "prod"}
	got := minimal.Env("prod")
	want = []string{"PGPORT=5432", "PGDATABASE=prod", "PGSSLMODE=prefer", "PGAPPNAME=odoo-operator-backup"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Env = %q\nwant %q", got, want)
	}
	for _, kv := range got {
		for _, k := range []string{"PGHOST=", "PGUSER=", "PGPASSWORD="} {
			if strings.HasPrefix(kv, k) {
				t.Errorf("Env sets %s although the config has none", k)
			}
		}
	}
}
