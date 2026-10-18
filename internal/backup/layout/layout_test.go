package layout

import (
	"fmt"
	"math/rand"
	"path"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSnapshotPath(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "/backup"},
		{[]string{DumpFile}, "/backup/dump.sql"},
		{[]string{ManifestFile}, "/backup/manifest.json"},
		{[]string{FilestoreDir, "ab", "abcdef"}, "/backup/filestore/ab/abcdef"},
		{[]string{RepairedDir, "ab/abcdef"}, "/backup/filestore-repaired/ab/abcdef"},
		{[]string{"a", "", "b/"}, "/backup/a/b"},
	}
	for _, c := range cases {
		if got := SnapshotPath(c.in...); got != c.want {
			t.Errorf("SnapshotPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHostAndTags(t *testing.T) {
	if got := Host("shop", "odoo"); got != "shop/odoo" {
		t.Fatalf("Host = %q", got)
	}
	got := Tags("shop", "odoo", "nightly-1", "scheduled")
	want := []string{"deployment=shop/odoo", "backup=nightly-1", "kind=scheduled"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Tags = %q, want %q", got, want)
	}
	for _, tag := range got {
		if strings.Contains(tag, ",") {
			t.Errorf("tag %q contains a comma, which restic splits --tag on", tag)
		}
	}
	if Repository("shop", "odoo") != "shop/odoo" {
		t.Fatal("Repository")
	}
}

func TestJoinStorePath(t *testing.T) {
	cases := []struct{ store, repo, want string }{
		{"", "ns/dep", "ns/dep"},
		{"", "", ""},
		{"backups", "ns/dep", "backups/ns/dep"},
		{"backups/", "ns/dep", "backups/ns/dep"},
		{"backups//odoo///", "ns/dep", "backups/odoo/ns/dep"},
		{"/srv/backups", "ns/dep", "/srv/backups/ns/dep"},
		{"/srv/backups/", "ns/dep", "/srv/backups/ns/dep"},
		{"/", "ns/dep", "/ns/dep"},
		{"/", "", "/"},
		{"/srv", "", "/srv"},
		{"srv/", "", "srv"},
		{"a", "/ns/dep/", "a/ns/dep"},
		{"./a", "ns/dep", "a/ns/dep"},
	}
	for _, c := range cases {
		if got := JoinStorePath(c.store, c.repo); got != c.want {
			t.Errorf("JoinStorePath(%q, %q) = %q, want %q", c.store, c.repo, got, c.want)
		}
	}
}

func TestSuffix(t *testing.T) {
	loc := time.FixedZone("UTC+3", 3*3600)
	at := time.Date(2025, 3, 9, 1, 2, 3, 999, loc)
	if got := Suffix(at); got != "20250308T220203" {
		t.Fatalf("Suffix = %q, want the UTC time", got)
	}
	if !ValidSuffix(Suffix(time.Now())) {
		t.Fatal("ValidSuffix rejects Suffix(now)")
	}
}

func TestValidSuffix(t *testing.T) {
	valid := []string{"20250101T000000", "19991231T235959", "20991231T235959"}
	invalid := []string{
		"", "20250101", "20250101T00000", "20250101T0000000", "20250101t000000", "20250101 000000",
		"2025-01-01T00:00:00", "20250101T000000Z", " 20250101T000000", "20250101T000000\n",
		"x20250101T000000", "2025010aT000000", "../../etc", "20250101T000000/..", "٢٠٢٥٠١٠١T000000",
	}
	for _, s := range valid {
		if !ValidSuffix(s) {
			t.Errorf("ValidSuffix(%q) = false", s)
		}
	}
	for _, s := range invalid {
		if ValidSuffix(s) {
			t.Errorf("ValidSuffix(%q) = true", s)
		}
	}
}

func TestPathsForSuffix(t *testing.T) {
	s := "20250101T000000"
	if got := JournalFile(s); got != ".odoo-operator/restore-20250101T000000.json" {
		t.Errorf("JournalFile = %q", got)
	}
	if got := StagingDir(s); got != ".odoo-operator/staging-20250101T000000" {
		t.Errorf("StagingDir = %q", got)
	}
	if got := FilestorePath("prod"); got != "filestore/prod" {
		t.Errorf("FilestorePath = %q", got)
	}
	if JournalFile(s) == StagingDir(s) {
		t.Error("journal and staging share a path")
	}
}

func TestNamesShortDatabase(t *testing.T) {
	s := "20250102T030405"
	if got := RestoreName("prod", s); got != "prod_restore_20250102T030405" {
		t.Errorf("RestoreName = %q", got)
	}
	if got := PrevName("prod", s); got != "prod_prev_20250102T030405" {
		t.Errorf("PrevName = %q", got)
	}
}

func TestNamesLongDatabase(t *testing.T) {
	s := "20250102T030405"
	db := strings.Repeat("d", 63)
	r, p := RestoreName(db, s), PrevName(db, s)
	if len(r) != 63 {
		t.Errorf("RestoreName of a 63-byte name is %d bytes, want exactly 63", len(r))
	}
	if !strings.HasSuffix(r, "_restore_"+s) || !strings.HasSuffix(p, "_prev_"+s) {
		t.Errorf("names lost their suffix: %q %q", r, p)
	}
	if strings.TrimSuffix(r, "_restore_"+s) != strings.TrimSuffix(p, "_prev_"+s) {
		t.Errorf("restore and prev names use different bases: %q %q", r, p)
	}
}

func TestNamesNeverSplitACharacter(t *testing.T) {
	s := "20250102T030405"
	for _, db := range []string{
		strings.Repeat("é", 40),             // 2-byte runes, boundary falls mid-rune
		"a" + strings.Repeat("中", 30),       // 3-byte runes
		"ab" + strings.Repeat("😀", 20),      // 4-byte runes
		strings.Repeat("x", 38) + "😀" + "y", // a rune straddling the limit
	} {
		for _, n := range []string{RestoreName(db, s), PrevName(db, s)} {
			if !utf8.ValidString(n) {
				t.Errorf("name %q for %q is not valid UTF-8", n, db)
			}
			if len(n) > 63 {
				t.Errorf("name %q is %d bytes", n, len(n))
			}
		}
	}
}

func TestCheckName(t *testing.T) {
	db, s := "prod", "20250102T030405"
	other := "20250102T030406"
	accept := []string{RestoreName(db, s), PrevName(db, s)}
	reject := []string{
		db, "", "postgres", "template0", "template1",
		RestoreName(db, other), PrevName(db, other),
		RestoreName("prod2", s), PrevName("pro", s),
		"prod_restore_" + s + "x", "prod_prev_", "../prod_prev_" + s, "prod_prev_" + s + "/..",
		"filestore/" + PrevName(db, s), strings.ToUpper(PrevName(db, s)),
	}
	for _, n := range accept {
		if err := CheckName(n, db, s); err != nil {
			t.Errorf("CheckName(%q) = %v", n, err)
		}
	}
	for _, n := range reject {
		if err := CheckName(n, db, s); err == nil {
			t.Errorf("CheckName(%q) accepted it", n)
		}
	}
	for _, bad := range []string{"", "x", "../20250102T030405", "20250102T030405/"} {
		if err := CheckName(RestoreName(db, bad), db, bad); err == nil {
			t.Errorf("CheckName accepted invalid suffix %q", bad)
		}
	}
}

// A live database named exactly like a restore copy must still never be
// accepted for deletion.
func TestCheckNameRefusesLiveNamedLikeACopy(t *testing.T) {
	s := "20250102T030405"
	for _, base := range []string{"prod", strings.Repeat("p", 39), strings.Repeat("p", 60)} {
		for _, live := range []string{RestoreName(base, s), PrevName(base, s)} {
			if err := CheckName(live, live, s); err == nil {
				t.Errorf("CheckName accepted the live database %q", live)
			}
		}
	}
}

func randomDB(rng *rand.Rand) string {
	alphabets := []string{
		"abcdefghijklmnopqrstuvwxyz0123456789_",
		"ABCxyz-_.$ ",
		"éàüßñ中文字😀ア",
		"_restore_prev_0123456789T",
	}
	n := rng.Intn(80) + 1
	var b strings.Builder
	runes := []rune(alphabets[rng.Intn(len(alphabets))])
	for b.Len() < n {
		b.WriteRune(runes[rng.Intn(len(runes))])
	}
	return b.String()
}

func randomSuffix(rng *rand.Rand) string {
	t := time.Unix(rng.Int63n(4102444800), 0) // 1970..2100
	return Suffix(t)
}

func TestNamesProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 20000; i++ {
		db, s := randomDB(rng), randomSuffix(rng)
		if !utf8.ValidString(db) {
			continue
		}
		r, p := RestoreName(db, s), PrevName(db, s)
		for _, n := range []string{r, p} {
			if n == db {
				t.Fatalf("%q: a name equals the live name", db)
			}
			if len(n) > 63 {
				t.Fatalf("%q: name %q is %d bytes", db, n, len(n))
			}
			if !utf8.ValidString(n) {
				t.Fatalf("%q: name %q is not valid UTF-8", db, n)
			}
			if err := CheckName(n, db, s); err != nil {
				t.Fatalf("%q: CheckName rejects its own name %q: %v", db, n, err)
			}
			if strings.ContainsAny(n, "/") != strings.ContainsAny(db, "/") {
				t.Fatalf("%q: name %q adds a path separator", db, n)
			}
		}
		if r == p {
			t.Fatalf("%q: restore and prev names are equal", db)
		}
		// Stable.
		if RestoreName(db, s) != r || PrevName(db, s) != p {
			t.Fatalf("%q: names are not stable", db)
		}
		// Another suffix gives other names, which CheckName refuses.
		s2 := randomSuffix(rng)
		if s2 != s {
			if RestoreName(db, s2) == r || PrevName(db, s2) == p {
				t.Fatalf("%q: two suffixes give the same name", db)
			}
			if CheckName(RestoreName(db, s2), db, s) == nil || CheckName(PrevName(db, s2), db, s) == nil {
				t.Fatalf("%q: CheckName accepts a name of suffix %s for %s", db, s2, s)
			}
		}
		if CheckName(db, db, s) == nil {
			t.Fatalf("CheckName accepts the live name %q", db)
		}
		if CheckName(path.Join("..", p), db, s) == nil {
			t.Fatalf("CheckName accepts a path for %q", db)
		}
	}
}

func TestNamesMaxLengthPerInputLength(t *testing.T) {
	s := "20250102T030405"
	for n := 0; n <= 200; n++ {
		db := strings.Repeat("a", n)
		r, p := RestoreName(db, s), PrevName(db, s)
		wantBase := n
		if n > 39 {
			wantBase = 39
		}
		if len(r) != wantBase+len("_restore_")+len(s) || len(p) != wantBase+len("_prev_")+len(s) {
			t.Fatalf("length %d: %q %q", n, r, p)
		}
	}
	if got := fmt.Sprint(len(RestoreName(strings.Repeat("a", 100), s))); got != "63" {
		t.Fatalf("restore name of a long db is %s bytes", got)
	}
}
