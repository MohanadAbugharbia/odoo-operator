package manifest

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSeries(t *testing.T) {
	cases := map[string]string{
		"18.0.1.3":       "18.0",
		"17.0.1.0.0":     "17.0",
		"16.0":           "16.0",
		"saas~18.2.1.0":  "saas~18.2",
		"saas~17.4":      "saas~17.4",
		"8.0.1.3":        "8.0",
		"":               "",
		"18":             "",
		"abc.def":        "",
		"18.x.1":         "",
		"x18.0.1":        "",
		"saas~x.2":       "",
		"saas~":          "",
		"18..1":          "",
		" 18.0":          "",
		"1.2.3.4.5.6.7":  "1.2",
		"18.0-rc.1.0.0":  "",
		"-1.0.1":         "-1.0", // Atoi accepts a sign; not a real version, documented here
		"saas~18.2~1.0.": "",
	}
	for in, want := range cases {
		if got := Series(in); got != want {
			t.Errorf("Series(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersionInfo(t *testing.T) {
	cases := []struct {
		series string
		want   []any
	}{
		{"18.0", []any{18, 0, 0, "final", 0, ""}},
		{"17.4", []any{17, 4, 0, "final", 0, ""}},
		{"saas~18.2", []any{"saas~18", 2, 0, "final", 0, ""}},
		{"", nil},
		{"18", nil},
	}
	for _, c := range cases {
		if got := VersionInfo(c.series); !reflect.DeepEqual(got, c.want) {
			t.Errorf("VersionInfo(%q) = %#v, want %#v", c.series, got, c.want)
		}
	}
}

func TestPgVersion(t *testing.T) {
	cases := map[int]string{
		160004: "16.0",
		170002: "17.0",
		180000: "18.0",
		90624:  "9.6",
		100023: "10.0",
		0:      "0.0",
	}
	for in, want := range cases {
		if got := PgVersion(in); got != want {
			t.Errorf("PgVersion(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestServerVersion(t *testing.T) {
	cases := map[int]string{
		160004: "16.4",
		170000: "17.0",
		180000: "18.0",
		180001: "18.1",
		100023: "10.23",
		130015: "13.15",
		90624:  "9.6.24",
		90500:  "9.5.0",
		80423:  "8.4.23",
		99999:  "9.99.99",
	}
	for in, want := range cases {
		if got := ServerVersion(in); got != want {
			t.Errorf("ServerVersion(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestNew(t *testing.T) {
	modules := map[string]string{"base": "18.0.1.3", "sale": "18.0.1.2"}
	m := New("prod", modules, 160004)
	want := Manifest{
		OdooDump: "1", DBName: "prod", Version: "18.0", VersionInfo: []any{18, 0, 0, "final", 0, ""},
		MajorVersion: "18.0", PgVersion: "16.0", Modules: modules,
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("New = %#v\nwant %#v", m, want)
	}
	if m.Series() != "18.0" {
		t.Fatalf("Series = %q", m.Series())
	}
	// Without base (not an Odoo database yet) the version fields are empty.
	empty := New("x", map[string]string{}, 170000)
	if empty.Version != "" || empty.MajorVersion != "" || empty.VersionInfo != nil || empty.Series() != "" {
		t.Fatalf("New without base = %#v", empty)
	}
}

func TestManifestSeriesFallbacks(t *testing.T) {
	cases := []struct {
		m    Manifest
		want string
	}{
		{Manifest{MajorVersion: "17.0", Version: "16.0", Modules: map[string]string{"base": "15.0.1"}}, "17.0"},
		{Manifest{Version: "16.0", Modules: map[string]string{"base": "15.0.1"}}, "15.0"},
		{Manifest{Version: "16.0", Modules: map[string]string{"base": "garbage"}}, "16.0"},
		{Manifest{Version: "16.0"}, "16.0"},
		{Manifest{}, ""},
	}
	for i, c := range cases {
		if got := c.m.Series(); got != c.want {
			t.Errorf("case %d: Series = %q, want %q", i, got, c.want)
		}
	}
}

func TestEncodeParseRoundTrip(t *testing.T) {
	m := New("prod", map[string]string{"base": "18.0.1.3", "web": "18.0.1.0"}, 160004)
	m.Operator = &Operator{
		Version: "v1.2.3", Namespace: "shop", Deployment: "odoo", Backup: "nightly", Kind: "scheduled",
		Image: "odoo:18", UpgradeToken: "tok", Consistency: "Online",
		SnapshotTime:       time.Date(2025, 1, 2, 3, 4, 5, 6000, time.UTC),
		PgServerVersionNum: 160004, PgDump: "pg_dump (PostgreSQL) 16.4",
		DumpSHA256: strings.Repeat("a", 64), DumpBytes: 12345,
		RowCounts: map[string]int64{"public.res_partner": 3, `public."Weird Table"`: 0},
		Files:     Files{Referenced: 10, Repaired: []string{"ab/abc"}, Missing: []string{"cd/cde"}},
	}
	data, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\n    \"odoo_dump\": \"1\"") {
		t.Fatalf("not indented with four spaces as Odoo writes it:\n%s", data)
	}
	back, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	// version_info comes back as JSON numbers.
	if !reflect.DeepEqual(back.VersionInfo, []any{18.0, 0.0, 0.0, "final", 0.0, ""}) {
		t.Fatalf("version_info = %#v", back.VersionInfo)
	}
	back.VersionInfo, m.VersionInfo = nil, nil
	if !reflect.DeepEqual(back, m) {
		t.Fatalf("round trip:\n got %#v\nwant %#v", back, m)
	}
	if back.Operator.SnapshotTime.Location() != time.UTC {
		t.Fatalf("snapshot time zone = %v", back.Operator.SnapshotTime.Location())
	}
}

func TestEncodeWithoutOperatorOmitsKey(t *testing.T) {
	m := New("prod", map[string]string{"base": "18.0.1.3"}, 160004)
	data, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["operator"]; ok {
		t.Fatal("an Odoo-only manifest carries an operator key")
	}
	for _, k := range []string{"odoo_dump", "db_name", "version", "version_info", "major_version", "pg_version", "modules"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("key %s is missing", k)
		}
	}
}

// What odoo/service/db.py dump_db_manifest writes (Odoo 17).
const odooManifest = `{
    "odoo_dump": "1",
    "db_name": "prod",
    "version": "17.0",
    "version_info": [17, 0, 0, "final", 0, ""],
    "major_version": "17.0",
    "pg_version": "15.6",
    "modules": {
        "base": "17.0.1.3",
        "web": "17.0.1.0",
        "sale": "17.0.1.2"
    }
}`

func TestParseOdooManifest(t *testing.T) {
	m, err := Parse([]byte(odooManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Operator != nil {
		t.Fatal("an Odoo manifest parsed with an operator part")
	}
	if m.DBName != "prod" || m.Series() != "17.0" || m.PgVersion != "15.6" || m.Modules["sale"] != "17.0.1.2" {
		t.Fatalf("parsed %#v", m)
	}
	// Older Odoo versions write no major_version.
	old := strings.Replace(odooManifest, `"major_version": "17.0",`, "", 1)
	m, err = Parse([]byte(old))
	if err != nil || m.Series() != "17.0" {
		t.Fatalf("without major_version: %q, %v", m.Series(), err)
	}
}

func TestParseOperatorOnly(t *testing.T) {
	m, err := Parse([]byte(`{"operator":{"version":"v1","rowCounts":{}}}`))
	if err != nil || m.Operator == nil || m.Operator.Version != "v1" {
		t.Fatalf("Parse = %#v, %v", m, err)
	}
}

func TestParseErrors(t *testing.T) {
	notOdoo := []string{`{}`, `{"db_name":"x"}`, `{"odoo_dump":""}`, `null`}
	for _, in := range notOdoo {
		if _, err := Parse([]byte(in)); !errors.Is(err, ErrNotOdoo) {
			t.Errorf("Parse(%s) = %v, want ErrNotOdoo", in, err)
		}
	}
	invalid := []string{``, `{`, `[]`, `"x"`, `{"odoo_dump":1}`, `{"modules":[]}`}
	for _, in := range invalid {
		_, err := Parse([]byte(in))
		if err == nil || errors.Is(err, ErrNotOdoo) {
			t.Errorf("Parse(%q) = %v, want a JSON error", in, err)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "manifest.json") {
			t.Errorf("Parse(%q) error %q does not name the file", in, err)
		}
	}
}

func TestCheckSeries(t *testing.T) {
	cases := []struct {
		backup, target string
		ok             bool
	}{
		{"18.0", "18.0", true},
		{"18.0", "", true},
		{"", "", true},
		{"", "18.0", false},
		{"17.0", "18.0", false},
		{"18.0", "17.0", false},
		{"saas~18.2", "18.0", false},
		{"saas~18.2", "saas~18.2", true},
		{"18.0", "18.0 ", false},
	}
	for _, c := range cases {
		err := CheckSeries(c.backup, c.target)
		if (err == nil) != c.ok {
			t.Errorf("CheckSeries(%q, %q) = %v, want ok=%v", c.backup, c.target, err, c.ok)
		}
		if err != nil && c.target != "" && !strings.Contains(err.Error(), c.target) {
			t.Errorf("CheckSeries(%q, %q) error does not name the target: %v", c.backup, c.target, err)
		}
	}
}
