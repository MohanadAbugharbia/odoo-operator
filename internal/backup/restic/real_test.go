package restic

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// realRestic returns a Restic for a new repository in a temporary folder,
// using the restic in TEST_RESTIC_BIN, /tmp/claude-0/bin/restic or PATH.
func realRestic(t *testing.T) (*Restic, string) {
	t.Helper()
	bin := os.Getenv("TEST_RESTIC_BIN")
	if bin == "" {
		if p, err := exec.LookPath("restic"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		t.Skip("no restic binary (set TEST_RESTIC_BIN)")
	}
	dir := t.TempDir()
	pw := filepath.Join(dir, "password")
	if err := os.WriteFile(pw, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	return &Restic{
		Bin: bin,
		Env: []string{
			"RESTIC_REPOSITORY=" + repo,
			"RESTIC_PASSWORD_FILE=" + pw,
			"RESTIC_CACHE_DIR=" + filepath.Join(dir, "cache"),
			"RESTIC_PASSWORD=", "RESTIC_PASSWORD_COMMAND=",
		},
		RetryLock: time.Second,
	}, dir
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestRealResticRoundTrip(t *testing.T) {
	r, dir := realRestic(t)
	ctx := context.Background()

	if ok, err := r.Exists(ctx); ok || err != nil {
		t.Fatalf("Exists before init = %v, %v", ok, err)
	}
	created, err := r.EnsureRepository(ctx)
	if !created || err != nil {
		t.Fatalf("EnsureRepository = %v, %v", created, err)
	}
	if created, err := r.EnsureRepository(ctx); created || err != nil {
		t.Fatalf("second EnsureRepository = %v, %v", created, err)
	}
	if err := r.Unlock(ctx); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dir, "src")
	files := map[string]string{
		"dump.sql":                 "CREATE TABLE x();\n",
		"manifest.json":            `{"odoo_dump":"1"}`,
		"filestore/ab/abcdef":      "attachment",
		"filestore/cd/cdef01":      "attachment 2",
		"[a]*?.txt":                "glob name",
		"ab.txt":                   "would match the unescaped glob",
		"aXY.txt":                  "would match too",
		"$HOME.txt":                "dollar",
		"#hash.txt":                "hash",
		"!bang.txt":                "bang",
		`back\slash.txt`:           "backslash",
		"filestore-repaired/ef/ef": "repaired",
	}
	writeTree(t, src, files)
	at := time.Date(2025, 5, 6, 7, 8, 9, 0, time.UTC)
	res, err := r.Backup(ctx, BackupOptions{
		Host: "ns/dep", Tags: []string{"deployment=ns/dep", "backup=b1", "kind=manual"}, Time: at,
	}, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.SnapshotID == "" || res.Summary.TotalFilesProcessed != int64(len(files)) || len(res.Unreadable) != 0 {
		t.Fatalf("backup result = %+v", res)
	}
	// A second snapshot of another instance in the same repository.
	if _, err := r.Backup(ctx, BackupOptions{Host: "ns/other", Tags: []string{"deployment=ns/other", "backup=b2"}}, src); err != nil {
		t.Fatal(err)
	}

	snaps, err := r.Snapshots(ctx, "ns/dep", "deployment=ns/dep")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].ID != res.Summary.SnapshotID || snaps[0].Hostname != "ns/dep" ||
		!snaps[0].HasTag("backup=b1") || !snaps[0].Time.Equal(at) || len(snaps[0].Paths) != 1 || snaps[0].Paths[0] != src {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if all, err := r.Snapshots(ctx, ""); err != nil || len(all) != 2 {
		t.Fatalf("all snapshots = %d, %v", len(all), err)
	}
	if none, err := r.Snapshots(ctx, "ns/dep", "deployment=ns/dep", "backup=b2"); err != nil || len(none) != 0 {
		t.Fatalf("snapshots with both tags = %+v, %v", none, err)
	}
	snap, err := r.Snapshot(ctx, snaps[0].ShortID)
	if err != nil || snap.ID != snaps[0].ID {
		t.Fatalf("Snapshot(short id) = %+v, %v", snap, err)
	}
	if _, err := r.Snapshot(ctx, "deadbeef"); err == nil {
		t.Fatal("Snapshot of an unknown id succeeded")
	}

	checkRealLsAndDump(t, r, snap.ID, src, files)
	checkRealRestores(t, r, snap.ID, src, dir, files)

	// Everything.
	target := filepath.Join(dir, "restore", "all")
	if err := r.Restore(ctx, snap.ID, target); err != nil {
		t.Fatal(err)
	}
	if got := listFiles(t, filepath.Join(target, src)); len(got) != len(files) {
		t.Fatalf("full restore = %q", got)
	}
}

// checkRealLsAndDump lists and dumps the files of snapshot snapID of src.
func checkRealLsAndDump(t *testing.T, r *Restic, snapID, src string, files map[string]string) {
	t.Helper()
	ctx := context.Background()
	var nodes []Node
	if err := r.Ls(ctx, snapID, src, func(n Node) { nodes = append(nodes, n) }); err != nil {
		t.Fatal(err)
	}
	seen := map[string]Node{}
	for _, n := range nodes {
		seen[n.Path] = n
	}
	for name, content := range files {
		n, ok := seen[filepath.Join(src, name)]
		if !ok || n.Type != "file" || n.Size != int64(len(content)) {
			t.Errorf("ls: %s = %+v (found %v)", name, n, ok)
		}
	}

	var buf bytes.Buffer
	if err := r.Dump(ctx, snapID, filepath.Join(src, "manifest.json"), &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != files["manifest.json"] {
		t.Fatalf("dump = %q", buf.String())
	}
	if err := r.Dump(ctx, snapID, filepath.Join(src, "nope"), &bytes.Buffer{}); err == nil {
		t.Fatal("Dump of a missing file succeeded")
	}
}

// checkRealRestores restores single files and folders of snapshot snapID.
func checkRealRestores(t *testing.T, r *Restic, snapID, src, dir string, files map[string]string) {
	t.Helper()
	ctx := context.Background()
	restoreCases := []struct {
		name     string
		includes []string
		want     []string
	}{
		{"glob characters match only themselves", []string{"[a]*?.txt"}, []string{"[a]*?.txt"}},
		{"dollar is not expanded", []string{"$HOME.txt"}, []string{"$HOME.txt"}},
		{"leading hash is not a comment", []string{"#hash.txt"}, []string{"#hash.txt"}},
		{"leading bang is not a negation", []string{"!bang.txt"}, []string{"!bang.txt"}},
		{"backslash", []string{`back\slash.txt`}, []string{`back\slash.txt`}},
		{"a folder brings its files", []string{"filestore", "filestore-repaired"},
			[]string{"filestore-repaired/ef/ef", "filestore/ab/abcdef", "filestore/cd/cdef01"}},
		{"a missing path restores nothing", []string{"filestore/zz/zz"}, nil},
	}
	for i, c := range restoreCases {
		t.Run(c.name, func(t *testing.T) {
			target := filepath.Join(dir, "restore", string(rune('a'+i)))
			incs := make([]string, len(c.includes))
			for j, inc := range c.includes {
				incs[j] = filepath.Join(src, inc)
			}
			if err := r.Restore(ctx, snapID, target, incs...); err != nil {
				t.Fatal(err)
			}
			got := listFiles(t, filepath.Join(target, src))
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("restored %q, want %q", got, c.want)
			}
			for _, name := range got {
				data, err := os.ReadFile(filepath.Join(target, src, name))
				if err != nil || string(data) != files[name] {
					t.Fatalf("%s restored as %q, %v", name, data, err)
				}
			}
		})
	}
}

func TestRealResticWrongPassword(t *testing.T) {
	r, dir := realRestic(t)
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Init(ctx); err == nil {
		t.Fatal("a second init succeeded")
	}
	wrong := filepath.Join(dir, "wrong")
	if err := os.WriteFile(wrong, []byte("nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := *r
	bad.Env = append(append([]string(nil), r.Env...), "RESTIC_PASSWORD_FILE="+wrong)
	ok, err := bad.Exists(ctx)
	if ok || !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("Exists with a wrong password = %v, %v", ok, err)
	}
	if created, err := bad.EnsureRepository(ctx); created || !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("EnsureRepository with a wrong password = %v, %v", created, err)
	}
	if _, err := bad.Snapshots(ctx, ""); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("Snapshots with a wrong password = %v", err)
	}
}

func TestRealResticLocked(t *testing.T) {
	r, _ := realRestic(t)
	ctx := context.Background()
	if err := r.Init(ctx); err != nil {
		t.Fatal(err)
	}
	// Hold a lock: a "restic backup --stdin" blocks on its input while it
	// holds a (non-exclusive) lock, which an exclusive "prune" cannot take.
	cmd := r.command(ctx, "backup", "--stdin", "--stdin-filename", "x")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	// Wait for its (non-exclusive) lock, then ask for an exclusive one.
	deadline := time.Now().Add(20 * time.Second)
	for {
		locked := *r
		locked.RetryLock = 0
		out, err := locked.run(ctx, "list", "locks", "--no-lock")
		if err != nil {
			t.Fatal(err)
		}
		if len(bytes.TrimSpace(out)) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("restic backup --stdin did not take a lock")
		}
		time.Sleep(100 * time.Millisecond)
	}
	locked := *r
	locked.RetryLock = 0
	start := time.Now()
	_, err = locked.run(ctx, "prune")
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("prune on a locked repository = %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("prune waited although RetryLock is 0")
	}
}
