package agent

// Tests of the helpers and guards that need no database. Helper names here
// start with "h" to stay clear of the integration tests' helpers.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/layout"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/odooconf"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/result"
)

func hWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hSHA1(content []byte) string {
	h := sha1.Sum(content)
	return hex.EncodeToString(h[:])
}

func hScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSafeStoreName(t *testing.T) {
	safe := []string{
		"ab/" + strings.Repeat("a", 40), "checklist/ab/abc", "a", "a.b", "a_b-c", "..a", "a..", ".hidden/x", "x/.y",
	}
	unsafe := []string{
		"", "/abs", "a/", "a//b", "..", ".", "../x", "x/..", "x/../y", "./x", "x/./y", `a\b`, "a b", "a\nb",
		"é/x", "a:b", "a*b", "C:/x", "a/b/", "\x00",
	}
	for _, n := range safe {
		if !safeStoreName(n) {
			t.Errorf("safeStoreName(%q) = false", n)
		}
	}
	for _, n := range unsafe {
		if safeStoreName(n) {
			t.Errorf("safeStoreName(%q) = true", n)
		}
	}
	// Whatever passes stays inside its folder.
	rng := rand.New(rand.NewSource(7))
	alphabet := "ab./_-"
	for i := 0; i < 20000; i++ {
		b := make([]byte, 1+rng.Intn(12))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		n := string(b)
		if !safeStoreName(n) {
			continue
		}
		joined := filepath.Join("/root/filestore", n)
		if !strings.HasPrefix(joined, "/root/filestore/") || filepath.Clean(joined) != joined {
			t.Fatalf("safe name %q escapes or is not clean: %s", n, joined)
		}
	}
}

func TestContentMatchesName(t *testing.T) {
	dir := t.TempDir()
	content := []byte("attachment content")
	sum := hSHA1(content)
	good := filepath.Join(dir, sum[:2], sum)
	hWrite(t, good, content)
	bad := filepath.Join(dir, "x", hSHA1([]byte("other")))
	hWrite(t, bad, content)
	other := filepath.Join(dir, "not-a-sha1.txt")
	hWrite(t, other, content)
	upper := filepath.Join(dir, strings.ToUpper(sum))
	hWrite(t, upper, []byte("anything"))

	cases := []struct {
		path string
		ok   bool
	}{
		{good, true}, {bad, false}, {other, true}, {upper, true},
		// A name that is not a sha1 is never read, so even a missing file passes.
		{filepath.Join(dir, "missing.bin"), true},
	}
	for _, c := range cases {
		ok, err := contentMatchesName(c.path)
		if err != nil || ok != c.ok {
			t.Errorf("contentMatchesName(%s) = %v, %v; want %v", filepath.Base(c.path), ok, err, c.ok)
		}
	}
	if _, err := contentMatchesName(filepath.Join(dir, strings.Repeat("0", 40))); err == nil {
		t.Error("a missing sha1-named file passed")
	}
	// A folder named like a sha1 cannot be read as a file.
	folder := filepath.Join(dir, strings.Repeat("f", 40))
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, err := contentMatchesName(folder); ok || err == nil {
		t.Errorf("folder = %v, %v", ok, err)
	}
}

func TestIsFileAndExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	hWrite(t, file, []byte("x"))
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "nope"), dangling); err != nil {
		t.Fatal(err)
	}
	if !isFile(file) || isFile(dir) || isFile(link) || isFile(filepath.Join(dir, "nope")) {
		t.Error("isFile")
	}
	for path, want := range map[string]bool{file: true, dir: true, link: true, dangling: true, filepath.Join(dir, "nope"): false} {
		got, err := exists(path)
		if err != nil || got != want {
			t.Errorf("exists(%s) = %v, %v", filepath.Base(path), got, err)
		}
	}
	// A path below a file is an error, not "does not exist".
	if _, err := exists(filepath.Join(file, "below")); err == nil {
		t.Error("exists below a file returned no error")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "journal.json")
	if err := writeFileAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "two" {
		t.Fatalf("content = %q, %v", data, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	// The parent is a file: nothing can be written.
	blocker := filepath.Join(dir, "blocker")
	hWrite(t, blocker, nil)
	if err := writeFileAtomic(filepath.Join(blocker, "x"), []byte("x")); err == nil {
		t.Fatal("wrote below a file")
	}
	// The target is a folder: the rename fails and the temp file is removed.
	target := filepath.Join(dir, "isdir")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("x")); err == nil {
		t.Fatal("replaced a folder")
	}
	entries, _ = os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
	if err := syncDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("syncDir of a missing folder succeeded")
	}
}

func TestSampleIndexes(t *testing.T) {
	cases := []struct {
		total, n int
		want     []int
	}{
		{0, 5, nil}, {-1, 5, nil}, {5, 0, nil}, {5, -1, nil},
		{3, 5, []int{0, 1, 2}}, {5, 5, []int{0, 1, 2, 3, 4}},
		{10, 2, []int{0, 5}}, {10, 3, []int{0, 3, 6}}, {1, 1, []int{0}},
	}
	for _, c := range cases {
		if got := sampleIndexes(c.total, c.n); !reflect.DeepEqual(got, c.want) {
			t.Errorf("sampleIndexes(%d, %d) = %v, want %v", c.total, c.n, got, c.want)
		}
	}
	for total := 1; total < 300; total++ {
		for _, n := range []int{1, 2, 7, 50, 200, 299, 500} {
			got := sampleIndexes(total, n)
			want := min(total, n)
			if len(got) != want {
				t.Fatalf("sampleIndexes(%d, %d) has %d entries", total, n, len(got))
			}
			for i, v := range got {
				if v < 0 || v >= total || (i > 0 && v <= got[i-1]) {
					t.Fatalf("sampleIndexes(%d, %d) = %v is not strictly increasing within range", total, n, got)
				}
			}
		}
	}
}

func TestLimitAndContains(t *testing.T) {
	list := []string{"a", "b", "c"}
	if !reflect.DeepEqual(limit(list, 2), []string{"a", "b"}) || !reflect.DeepEqual(limit(list, 3), list) ||
		!reflect.DeepEqual(limit(list, 10), list) || len(limit(list, 0)) != 0 || limit(nil, 3) != nil {
		t.Error("limit")
	}
	if !contains(list, "b") || contains(list, "d") || contains(nil, "") {
		t.Error("contains")
	}
}

func TestDiffCounts(t *testing.T) {
	want := map[string]int64{"public.a": 1, "public.b": 2, "public.c": 0}
	if d := diffCounts(want, map[string]int64{"public.a": 1, "public.b": 2, "public.c": 0}); d != nil {
		t.Fatalf("equal counts differ: %q", d)
	}
	got := diffCounts(want, map[string]int64{"public.a": 1, "public.b": 3, "public.d": 0})
	expect := []string{
		"table public.b has 3 rows instead of 2",
		"table public.c is missing",
		"table public.d is not in the backup",
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("diffCounts = %q", got)
	}
	if d := diffCounts(nil, nil); d != nil {
		t.Fatalf("empty = %q", d)
	}
	if d := diffCounts(map[string]int64{"x": 0}, nil); len(d) != 1 {
		t.Fatalf("missing table = %q", d)
	}
}

func TestMissingModules(t *testing.T) {
	installed := map[string]string{"base": "18.0.1.3", "sale": "18.0.1.0"}
	if got := missingModules([]string{"stock", "sale", "account", "base"}, installed); !reflect.DeepEqual(got, []string{"stock", "account"}) {
		t.Fatalf("missingModules = %q", got)
	}
	if got := missingModules(nil, installed); got != nil {
		t.Fatalf("no expectations = %q", got)
	}
	if got := missingModules([]string{"base"}, nil); !reflect.DeepEqual(got, []string{"base"}) {
		t.Fatalf("nothing installed = %q", got)
	}
}

func TestJournalReadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, layout.JournalFile("20250101T000000"))
	j, err := readJournal(path)
	if j != nil || err != nil {
		t.Fatalf("missing journal = %+v, %v", j, err)
	}
	comment := "odoo-operator:shop/odoo"
	want := &journal{
		Version: journalVersion, Mode: ModeRestore, Suffix: "20250101T000000", Stage: stageSwapping,
		Source: "prod_restore_20250101T000000", SourceDB: true, SourceFS: true, HadLiveDB: true,
		LiveComment: &comment, Image: "odoo:18", UpgradeToken: "t", OdooVersion: "18.0",
		MissingModules: []string{"x"}, Warnings: []string{"w"},
	}
	if err := want.save(path); err != nil {
		t.Fatal(err)
	}
	got, err := readJournal(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	hWrite(t, path, []byte("{not json"))
	if _, err := readJournal(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("unreadable journal = %v", err)
	}
	if _, err := readJournal(dir); err == nil {
		t.Fatal("reading a folder as a journal succeeded")
	}
}

func TestPrepare(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if err := Prepare(root, "prod"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(root, "filestore", "prod"))
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o755&^hUmask() {
		t.Fatalf("created %v, %v", st.Mode(), err)
	}
	// An existing folder is left exactly as it is.
	existing := filepath.Join(root, "filestore", "other")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	hWrite(t, filepath.Join(existing, "ab", "file"), []byte("keep"))
	if err := os.Chmod(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(root, "other"); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(existing)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("existing folder mode changed to %v", st.Mode())
	}
	if data, _ := os.ReadFile(filepath.Join(existing, "ab", "file")); string(data) != "keep" {
		t.Fatal("existing content changed")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	hWrite(t, blocker, nil)
	if err := Prepare(blocker, "prod"); err == nil {
		t.Fatal("Prepare below a file succeeded")
	}
}

func hUmask() os.FileMode {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("umask-probe-%d", time.Now().UnixNano()))
	if err := os.Mkdir(dir, 0o777); err != nil {
		return 0o022
	}
	defer func() { _ = os.Remove(dir) }()
	st, err := os.Stat(dir)
	if err != nil {
		return 0o022
	}
	return 0o777 &^ st.Mode().Perm()
}

func TestTail(t *testing.T) {
	if tail("  short \n") != "short" {
		t.Fatal("short")
	}
	long := "HEAD" + strings.Repeat("x", 3000) + "END"
	got := tail(long)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "END") || strings.Contains(got, "HEAD") || len(got) != 1500+len("…") {
		t.Fatalf("tail = %d bytes", len(got))
	}
}

func TestCountingWriter(t *testing.T) {
	c := &countingWriter{}
	for _, s := range []string{"abc", "", "de"} {
		n, err := c.Write([]byte(s))
		if err != nil || n != len(s) {
			t.Fatal(n, err)
		}
	}
	if c.n != 5 {
		t.Fatalf("n = %d", c.n)
	}
}

func TestWalkFilestore(t *testing.T) {
	dir := t.TempDir()
	files, size, err := walkFilestore(filepath.Join(dir, "missing"))
	if files != 0 || size != 0 || err != nil {
		t.Fatalf("missing = %d %d %v", files, size, err)
	}
	fs := filepath.Join(dir, "fs")
	hWrite(t, filepath.Join(fs, "ab", "1"), []byte("123"))
	hWrite(t, filepath.Join(fs, "cd", "2"), []byte("4567"))
	hWrite(t, filepath.Join(fs, "empty"), nil)
	if err := os.MkdirAll(filepath.Join(fs, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fs, "ab", "1"), filepath.Join(fs, "link")); err != nil {
		t.Fatal(err)
	}
	files, size, err = walkFilestore(fs)
	if files != 3 || size != 7 || err != nil {
		t.Fatalf("walk = %d files %d bytes %v", files, size, err)
	}
}

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	hWrite(t, src, []byte("content"))
	dst := filepath.Join(dir, "dst")
	if err := moveFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "content" {
		t.Fatal("content")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source still there after a rename")
	}
	if err := moveFile(filepath.Join(dir, "nope"), filepath.Join(dir, "x")); err == nil {
		t.Fatal("moving a missing file succeeded")
	}
	// Rename fails (the target is a non-empty folder) and so does the copy.
	busy := filepath.Join(dir, "busy")
	hWrite(t, filepath.Join(busy, "child"), nil)
	hWrite(t, src, []byte("again"))
	if err := moveFile(src, busy); err == nil {
		t.Fatal("moved a file onto a folder")
	}
}

func TestMoveFileAcrossFilesystems(t *testing.T) {
	shm := "/dev/shm"
	if st, err := os.Stat(shm); err != nil || !st.IsDir() {
		t.Skip("no /dev/shm")
	}
	other, err := os.MkdirTemp(shm, "move-test-")
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = os.RemoveAll(other) }()
	src := filepath.Join(other, "src")
	hWrite(t, src, []byte("across"))
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.Rename(src, filepath.Join(filepath.Dir(dst), "probe")); err == nil {
		t.Skip("/dev/shm is on the same filesystem as the temp folder")
	}
	if err := moveFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "across" {
		t.Fatalf("copied %q", data)
	}
}

func TestEnvHelpers(t *testing.T) {
	e := &Env{}
	if d := time.Since(e.now()); d < 0 || d > time.Minute {
		t.Fatal("now without a clock")
	}
	fixed := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return fixed }
	if !e.now().Equal(fixed) {
		t.Fatal("now with a clock")
	}
	e.logf("no logger: %d", 1) // must not panic
	if err := e.fault(FaultBackupDumped); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	e.Log = NewLogger(&buf)
	e.logf("hello %s", "world")
	if !strings.HasSuffix(buf.String(), "hello world\n") {
		t.Fatalf("log = %q", buf.String())
	}
	if e.Log.Flags()&log.LUTC == 0 {
		t.Fatal("the logger does not log in UTC")
	}
	var points []string
	e.Fault = func(p string) error {
		points = append(points, p)
		if p == FaultSwapStarted {
			return errors.New("crash")
		}
		return nil
	}
	if e.fault(FaultBackupUploaded) != nil || e.fault(FaultSwapStarted) == nil {
		t.Fatal("fault")
	}
	if !reflect.DeepEqual(points, []string{FaultBackupUploaded, FaultSwapStarted}) {
		t.Fatalf("points = %q", points)
	}
}

// ---- guards that stop before any database connection ----------------------

const hSuffix = "20250102T030405"

// hUnreachable is a config whose connections fail at once.
func hUnreachable() odooconf.Config {
	return odooconf.Config{Host: "127.0.0.1", Port: 1, User: "odoo", Name: "prod", SSLMode: "disable"}
}

func hEnv() *Env {
	return &Env{Config: hUnreachable(), MaintenanceDB: "postgres", Restic: &restic.Restic{Bin: "/nonexistent/restic"}}
}

func TestRestoreGuards(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	cases := []struct {
		name string
		o    RestoreOptions
		msg  string
	}{
		{"bad suffix", RestoreOptions{Mode: ModeRestore, Suffix: "../x", DataRoot: data}, "invalid restore suffix"},
		{"empty suffix", RestoreOptions{Mode: ModeRestore, DataRoot: data}, "invalid restore suffix"},
		{"bad undo suffix", RestoreOptions{Mode: ModeUndo, Suffix: hSuffix, UndoSuffix: "x", DataRoot: data}, "invalid restore suffix"},
		{"unknown mode", RestoreOptions{Mode: "rewind", Suffix: hSuffix, DataRoot: data}, "unknown restore mode"},
	}
	for _, c := range cases {
		res := Restore(ctx, hEnv(), c.o)
		if res.OK || res.Step != result.StepConnect || !strings.Contains(res.Error, c.msg) || res.LiveChanged {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
	entries, _ := os.ReadDir(data)
	if len(entries) != 0 {
		t.Fatalf("a refused restore wrote %v", entries)
	}
}

func TestRestoreJournalGuards(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	path := filepath.Join(data, layout.JournalFile(hSuffix))

	hWrite(t, path, []byte("garbage"))
	res := Restore(ctx, hEnv(), RestoreOptions{Mode: ModeRestore, Suffix: hSuffix, DataRoot: data})
	if res.OK || !strings.Contains(res.Error, "unreadable") {
		t.Fatalf("unreadable journal: %+v", res)
	}

	if err := (&journal{Version: 1, Mode: ModeUndo, Suffix: hSuffix}).save(path); err != nil {
		t.Fatal(err)
	}
	res = Restore(ctx, hEnv(), RestoreOptions{Mode: ModeRestore, Suffix: hSuffix, DataRoot: data})
	if res.OK || !strings.Contains(res.Error, "journal is for a undo, not a restore") {
		t.Fatalf("mode mismatch: %+v", res)
	}
}

func TestRestoreConnectFailureReportsLiveState(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		stage string
		live  bool
	}{{stageNew, false}, {stageRestored, false}, {stageSwapping, true}, {stageSwapped, true}} {
		data := t.TempDir()
		path := filepath.Join(data, layout.JournalFile(hSuffix))
		if c.stage != stageNew {
			if err := (&journal{Version: 1, Mode: ModeRestore, Suffix: hSuffix, Stage: c.stage}).save(path); err != nil {
				t.Fatal(err)
			}
		}
		res := Restore(ctx, hEnv(), RestoreOptions{Mode: ModeRestore, Suffix: hSuffix, Snapshot: "x", DataRoot: data})
		if res.OK || res.Step != result.StepConnect || res.LiveChanged != c.live {
			t.Errorf("stage %q: %+v", c.stage, res)
		}
	}
}

func TestCleanupGuards(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	prevFS := filepath.Join(data, layout.FilestorePath(layout.PrevName("prod", hSuffix)))
	liveFS := filepath.Join(data, layout.FilestorePath("prod"))
	hWrite(t, filepath.Join(prevFS, "ab", "x"), []byte("prev"))
	hWrite(t, filepath.Join(liveFS, "ab", "x"), []byte("live"))

	for _, bad := range []string{"", "x", "../" + hSuffix, hSuffix + "/.."} {
		res := Cleanup(ctx, hEnv(), data, bad)
		if res.OK || res.Step != result.StepCleanup || !strings.Contains(res.Error, "invalid restore suffix") {
			t.Errorf("suffix %q: %+v", bad, res)
		}
	}

	path := filepath.Join(data, layout.JournalFile(hSuffix))
	hWrite(t, path, []byte("{"))
	if res := Cleanup(ctx, hEnv(), data, hSuffix); res.OK || res.Step != result.StepCleanup {
		t.Errorf("unreadable journal: %+v", res)
	}

	if err := (&journal{Version: 1, Mode: ModeRestore, Suffix: hSuffix, Stage: stageSwapping}).save(path); err != nil {
		t.Fatal(err)
	}
	res := Cleanup(ctx, hEnv(), data, hSuffix)
	if res.OK || res.Step != result.StepCleanup || !strings.Contains(res.Error, "has not finished its swap") {
		t.Errorf("swapping: %+v", res)
	}

	// Past the guards it needs the database; nothing is deleted before.
	if err := (&journal{Version: 1, Mode: ModeRestore, Suffix: hSuffix, Stage: stageSwapped}).save(path); err != nil {
		t.Fatal(err)
	}
	if res := Cleanup(ctx, hEnv(), data, hSuffix); res.OK || res.Step != result.StepConnect {
		t.Errorf("unreachable database: %+v", res)
	}
	for _, p := range []string{filepath.Join(prevFS, "ab", "x"), filepath.Join(liveFS, "ab", "x"), path} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted: %v", p, err)
		}
	}
}

// ---- verifySnapshot and repair with a fake restic ---------------------------

// hFakeRestic prints lsFile for "ls" and, for "restore", copies tree into
// the --target folder. It exits with exit.
func hFakeRestic(t *testing.T, lsFile, tree string, exit int) *restic.Restic {
	t.Helper()
	bin := hScript(t, `sub=""; target=""; prev=""
for a in "$@"; do
  if [ -z "$sub" ]; then case "$a" in -*) ;; *) sub="$a";; esac; fi
  if [ "$prev" = "--target" ]; then target="$a"; fi
  prev="$a"
done
case "$sub" in
  ls) cat "$FAKE_LS" ;;
  restore) if [ -n "$FAKE_TREE" ]; then mkdir -p "$target" && cp -R "$FAKE_TREE/." "$target/"; fi ;;
esac
exit "$FAKE_EXIT"
`)
	return &restic.Restic{Bin: bin, Env: []string{"FAKE_LS=" + lsFile, "FAKE_TREE=" + tree, fmt.Sprintf("FAKE_EXIT=%d", exit)}}
}

func hNode(path, typ string) string {
	return fmt.Sprintf(`{"struct_type":"node","name":%q,"type":%q,"path":%q}`, filepath.Base(path), typ, path)
}

func TestVerifySnapshot(t *testing.T) {
	ctx := context.Background()
	root := "/backup"
	lines := []string{
		`{"struct_type":"snapshot","id":"x"}`,
		hNode("/backup", "dir"),
		hNode("/backup/dump.sql", "file"),
		hNode("/backup/manifest.json", "file"),
		hNode("/backup/filestore", "dir"),
		hNode("/backup/filestore/ab", "dir"),
		hNode("/backup/filestore/ab/present", "file"),
		hNode("/backup/filestore/ab/dirnotfile", "dir"),
		hNode("/backup/filestore-repaired/ef/repaired-ok", "file"),
		hNode("/elsewhere/filestore/ab/lost", "file"), // outside root: does not count
	}
	ls := filepath.Join(t.TempDir(), "ls.json")
	hWrite(t, ls, []byte(strings.Join(lines, "\n")+"\n"))
	e := &Env{Restic: hFakeRestic(t, ls, "", 0)}
	names := []string{"ab/present", "ab/lost", "ab/dirnotfile", "cd/known", "../unsafe", "ef/repaired-ok", "gh/repaired-gone"}
	lost, err := verifySnapshot(ctx, e, "snap", root, names,
		[]string{"cd/known"}, []string{"../unsafe"}, []string{"ef/repaired-ok", "gh/repaired-gone"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lost, []string{"ab/lost", "ab/dirnotfile", "gh/repaired-gone"}) {
		t.Fatalf("lost = %q", lost)
	}

	for _, missing := range []string{"/backup/dump.sql", "/backup/manifest.json"} {
		var kept []string
		for _, l := range lines {
			if !strings.Contains(l, `"path":"`+missing+`"`) {
				kept = append(kept, l)
			}
		}
		// The same name as a folder does not count either.
		kept = append(kept, hNode(missing, "dir"))
		hWrite(t, ls, []byte(strings.Join(kept, "\n")+"\n"))
		if _, err := verifySnapshot(ctx, e, "snap", root, nil, nil, nil, nil); err == nil ||
			!strings.Contains(err.Error(), filepath.Base(missing)) {
			t.Errorf("without %s: %v", missing, err)
		}
	}

	failing := &Env{Restic: hFakeRestic(t, ls, "", 1)}
	if _, err := verifySnapshot(ctx, failing, "snap", root, nil, nil, nil, nil); err == nil {
		t.Error("a failed ls verified")
	}
}

func TestRepairEarlyReturns(t *testing.T) {
	ctx := context.Background()
	e := &Env{Restic: &restic.Restic{Bin: "/nonexistent/restic"}}
	repaired, still, err := repair(ctx, e, t.TempDir(), []restic.Snapshot{{ID: "x", Paths: []string{"/backup"}}}, nil)
	if repaired != nil || still != nil || err != nil {
		t.Fatalf("nothing missing = %v %v %v", repaired, still, err)
	}
	missing := []string{"ab/x"}
	repaired, still, err = repair(ctx, e, t.TempDir(), nil, missing)
	if repaired != nil || !reflect.DeepEqual(still, missing) || err != nil {
		t.Fatalf("no previous backup = %v %v %v", repaired, still, err)
	}
	repaired, still, err = repair(ctx, e, t.TempDir(), []restic.Snapshot{{ID: "x", Paths: []string{"/a", "/b"}}}, missing)
	if repaired != nil || !reflect.DeepEqual(still, missing) || err != nil {
		t.Fatalf("previous with two paths = %v %v %v", repaired, still, err)
	}
	// restic fails.
	if _, _, err := repair(ctx, e, t.TempDir(), []restic.Snapshot{{ID: "x", ShortID: "x", Paths: []string{"/backup"}}}, missing); err == nil {
		t.Fatal("a failed restic restore repaired")
	}
}

func TestRepairFromPreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	good := []byte("good attachment")
	goodName := hSHA1(good)
	fromRepaired := []byte("repaired earlier")
	fromRepairedName := hSHA1(fromRepaired)
	damagedName := hSHA1([]byte("what it should be"))
	plain := "legacy/name.bin"

	// What the previous snapshot holds, under its root /backup.
	tree := t.TempDir()
	hWrite(t, filepath.Join(tree, "backup", "filestore", goodName[:2], goodName), good)
	hWrite(t, filepath.Join(tree, "backup", "filestore-repaired", fromRepairedName[:2], fromRepairedName), fromRepaired)
	hWrite(t, filepath.Join(tree, "backup", "filestore", damagedName[:2], damagedName), []byte("damaged"))
	hWrite(t, filepath.Join(tree, "backup", "filestore", plain), []byte("cannot be checked"))

	root := t.TempDir()
	e := &Env{Restic: hFakeRestic(t, "/dev/null", tree, 0)}
	missing := []string{
		goodName[:2] + "/" + goodName,
		fromRepairedName[:2] + "/" + fromRepairedName,
		damagedName[:2] + "/" + damagedName,
		"zz/" + strings.Repeat("0", 40),
		plain,
	}
	prev := []restic.Snapshot{
		{ID: "old", ShortID: "old", Paths: []string{"/elsewhere"}},
		{ID: "latest", ShortID: "latest", Paths: []string{"/backup"}},
	}
	repaired, still, err := repair(ctx, e, root, prev, missing)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repaired, []string{missing[0], missing[1], plain}) {
		t.Fatalf("repaired = %q", repaired)
	}
	if !reflect.DeepEqual(still, []string{missing[2], missing[3]}) {
		t.Fatalf("still missing = %q", still)
	}
	for _, n := range repaired {
		if _, err := os.Stat(filepath.Join(root, layout.RepairedDir, n)); err != nil {
			t.Errorf("%s not in filestore-repaired: %v", n, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, layout.RepairedDir, missing[0])); !bytes.Equal(data, good) {
		t.Errorf("repaired content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, layout.RepairedDir, missing[2])); !os.IsNotExist(err) {
		t.Error("a damaged file was taken")
	}
	// Only filestore-repaired/ was written to.
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != layout.RepairedDir {
		t.Errorf("root holds %v", entries)
	}
}

// ---- Probe with a fake rclone --------------------------------------------------

// hRclone handles rcat, cat and deletefile against local paths; FAIL_<sub>
// makes that command fail, CORRUPT makes cat return other content.
const hRclone = `log="$RCLONE_LOG"; echo "$*" >> "$log"
sub="$5"; file="$6"
case "$sub" in
  rcat) [ -n "$FAIL_RCAT" ] && { echo "write denied" >&2; exit 1; }; cat > "$file" ;;
  cat) [ -n "$FAIL_CAT" ] && { echo "read denied" >&2; exit 1; }
       if [ -n "$CORRUPT" ]; then echo corrupted; else cat "$file"; fi ;;
  deletefile) [ -n "$FAIL_DELETEFILE" ] && { echo "delete denied" >&2; exit 1; }; rm "$file" ;;
  lsf) [ -n "$FAIL_LSF" ] && { echo "list denied" >&2; exit 1; }; ls ;;
  *) exit 9 ;;
esac
`

func TestProbe(t *testing.T) {
	bin := hScript(t, hRclone)
	cases := []struct {
		name, env, msg string
	}{
		{"ok", "", ""},
		{"write fails", "FAIL_RCAT", "cannot write to the store"},
		{"read fails", "FAIL_CAT", "cannot read back from the store"},
		{"different content", "CORRUPT", "different content"},
		{"delete fails", "FAIL_DELETEFILE", "cannot delete from the store"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := t.TempDir()
			logFile := filepath.Join(t.TempDir(), "log")
			t.Setenv("RCLONE_LOG", logFile)
			for _, k := range []string{"FAIL_RCAT", "FAIL_CAT", "CORRUPT", "FAIL_DELETEFILE"} {
				t.Setenv(k, "")
			}
			if c.env != "" {
				t.Setenv(c.env, "1")
			}
			res := Probe(context.Background(), ProbeOptions{Rclone: bin, Target: target + "/"})
			if c.msg == "" {
				if !res.OK {
					t.Fatalf("probe failed: %+v", res)
				}
			} else if res.OK || res.Step != result.StepProbe || !strings.Contains(res.Error, c.msg) {
				t.Fatalf("probe = %+v, want %q", res, c.msg)
			}
			data, _ := os.ReadFile(logFile)
			calls := strings.Split(strings.TrimSpace(string(data)), "\n")
			var file string
			for _, call := range calls {
				f := strings.Fields(call)
				if len(f) != 6 || !slices.Equal(f[:4], []string{"--retries", "1", "--low-level-retries", "2"}) {
					t.Fatalf("call %q", call)
				}
				if file == "" {
					file = f[5]
				}
				if f[5] != file {
					t.Fatalf("calls touch different files: %q", calls)
				}
			}
			if !strings.HasPrefix(file, target+"/.odoo-operator-probe-") || strings.Contains(file, "//") {
				t.Fatalf("probe file %q", file)
			}
			if c.env != "FAIL_RCAT" && len(calls) != 3 {
				t.Fatalf("calls = %q (the file must be deleted even when reading fails)", calls)
			}
			if c.env == "" || c.env == "FAIL_CAT" || c.env == "CORRUPT" {
				if entries, _ := os.ReadDir(target); len(entries) != 0 {
					t.Fatalf("probe left %v", entries)
				}
			}
		})
	}
}

// A read-only store is only listed, never written.
func TestProbeReadOnly(t *testing.T) {
	bin := hScript(t, hRclone)
	for _, fail := range []bool{false, true} {
		target := t.TempDir()
		logFile := filepath.Join(t.TempDir(), "log")
		t.Setenv("RCLONE_LOG", logFile)
		t.Setenv("FAIL_LSF", map[bool]string{true: "1"}[fail])
		res := Probe(context.Background(), ProbeOptions{Rclone: bin, Target: target + "/", ReadOnly: true})
		if fail != !res.OK || (fail && !strings.Contains(res.Error, "cannot list the store")) {
			t.Fatalf("probe (list fails: %v) = %+v", fail, res)
		}
		data, _ := os.ReadFile(logFile)
		if calls := strings.Split(strings.TrimSpace(string(data)), "\n"); len(calls) != 1 || !strings.Contains(calls[0], " lsf ") {
			t.Fatalf("calls = %q", calls)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Fatalf("probe wrote %v", entries)
		}
	}
}

func TestProbeMissingRclone(t *testing.T) {
	res := Probe(context.Background(), ProbeOptions{Rclone: filepath.Join(t.TempDir(), "nope"), Target: t.TempDir()})
	if res.OK || !strings.Contains(res.Error, "cannot write") {
		t.Fatalf("= %+v", res)
	}
}

func TestKnownHostAlgorithms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	content := "# comment\n\n[sftp.example.com]:2222 ssh-ed25519 AAAA\nsftp.example.com ssh-rsa AAAB\n" +
		"@cert-authority *.example.com ecdsa-sha2-nistp256 AAAC\nbroken\nother ssh-ed25519 AAAD\nx unknown-type AAAE\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := knownHostAlgorithms(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "ssh-ed25519 rsa-sha2-512 rsa-sha2-256 ssh-rsa ecdsa-sha2-nistp256"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
	if got, err := knownHostAlgorithms(filepath.Join(t.TempDir(), "absent")); err != nil || got != nil {
		t.Fatalf("absent file: %v %v", got, err)
	}

	t.Setenv(envKnownHosts, path)
	t.Setenv(envHostKeyAlgorithms, "")
	if err := LimitHostKeyAlgorithms(); err != nil || os.Getenv(envHostKeyAlgorithms) != want {
		t.Fatalf("LimitHostKeyAlgorithms: %v, env %q", err, os.Getenv(envHostKeyAlgorithms))
	}
	t.Setenv(envHostKeyAlgorithms, "ssh-ed25519")
	if err := LimitHostKeyAlgorithms(); err != nil || os.Getenv(envHostKeyAlgorithms) != "ssh-ed25519" {
		t.Fatal("an explicit setting must be kept")
	}
	t.Setenv(envKnownHosts, "")
	t.Setenv(envHostKeyAlgorithms, "")
	if err := LimitHostKeyAlgorithms(); err != nil || os.Getenv(envHostKeyAlgorithms) != "" {
		t.Fatal("without known_hosts nothing is set")
	}

	t.Setenv("RCLONE_CONFIG_COPY2_KNOWN_HOSTS_FILE", path)
	t.Setenv("RCLONE_CONFIG_COPY2_HOST_KEY_ALGORITHMS", "")
	if err := LimitHostKeyAlgorithms(); err != nil || os.Getenv("RCLONE_CONFIG_COPY2_HOST_KEY_ALGORITHMS") != want {
		t.Fatalf("a copy store's remote is limited too: %v", err)
	}
}

func TestUseSSHAuthPrivateKey(t *testing.T) {
	dir := t.TempDir()
	authKey := filepath.Join(dir, sshAuthPrivateKey)
	if err := os.WriteFile(authKey, []byte("key"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RCLONE_CONFIG_STORE_KEY_FILE", filepath.Join(dir, "id_ed25519"))
	if err := UseSSHAuthPrivateKey(); err != nil || os.Getenv("RCLONE_CONFIG_STORE_KEY_FILE") != authKey {
		t.Fatalf("a missing id_ed25519 falls back to ssh-privatekey: %v, %q", err, os.Getenv("RCLONE_CONFIG_STORE_KEY_FILE"))
	}

	// id_ed25519 wins when both are there.
	ed := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(ed, []byte("key"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RCLONE_CONFIG_COPY1_KEY_FILE", ed)
	// Neither there: the setting is kept, and rclone names the missing file.
	other := filepath.Join(t.TempDir(), "id_ed25519")
	t.Setenv("RCLONE_CONFIG_COPY2_KEY_FILE", other)
	if err := UseSSHAuthPrivateKey(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("RCLONE_CONFIG_COPY1_KEY_FILE"); got != ed {
		t.Errorf("both keys: %q", got)
	}
	if got := os.Getenv("RCLONE_CONFIG_COPY2_KEY_FILE"); got != other {
		t.Errorf("no key: %q", got)
	}
}
