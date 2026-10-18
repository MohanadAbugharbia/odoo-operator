package restic

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBin(t *testing.T) {
	if (&Restic{}).bin() != "restic" || (&Restic{Bin: "/x/restic"}).bin() != "/x/restic" {
		t.Fatal("bin")
	}
}

func TestErrorMessage(t *testing.T) {
	e := &Error{Args: []string{"cat", "config"}, ExitCode: 1, Stderr: "  Fatal: boom\n"}
	if got := e.Error(); got != "restic cat config: exit 1: Fatal: boom" {
		t.Fatalf("Error() = %q", got)
	}
	long := &Error{Args: []string{"backup"}, ExitCode: 1, Stderr: "HEAD" + strings.Repeat("x", 5000) + "TAIL"}
	msg := long.Error()
	if strings.Contains(msg, "HEAD") || !strings.HasSuffix(msg, "TAIL") || len(msg) > 2100 {
		t.Fatalf("long stderr not cut to its end: %d bytes", len(msg))
	}
}

func TestOptionsRetryLockAndEnvComeFirst(t *testing.T) {
	f := newFake(t)
	f.r.Options = []string{"s3.bucket-lookup=path", "s3.region=eu-1"}
	f.r.RetryLock = 31 * time.Minute
	f.r.Env = append(f.r.Env, "RESTIC_REPOSITORY=s3:host/bucket/ns/dep", "RESTIC_PASSWORD_FILE=/secrets/restic/password")
	if err := f.r.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"-o", "s3.bucket-lookup=path", "-o", "s3.region=eu-1", "--retry-lock", "31m0s", "unlock"}
	if got := f.args(1); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	env := f.read("env.1")
	for _, kv := range []string{"RESTIC_REPOSITORY=s3:host/bucket/ns/dep", "RESTIC_PASSWORD_FILE=/secrets/restic/password"} {
		if !strings.Contains(env, kv) {
			t.Errorf("env lacks %s:\n%s", kv, env)
		}
	}
}

func TestNoRetryLockByDefault(t *testing.T) {
	f := newFake(t)
	if err := f.r.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.args(1); !reflect.DeepEqual(got, []string{"init"}) {
		t.Fatalf("args = %q", got)
	}
}

func TestLogReceivesStderr(t *testing.T) {
	f := newFake(t)
	var log bytes.Buffer
	f.r.Log = &log
	f.on("unlock", 0, "stdout-not-logged", "progress line\n", 0)
	if err := f.r.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if log.String() != "progress line\n" {
		t.Fatalf("log = %q", log.String())
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		exit    int
		want    error
		isError bool
	}{
		{1, nil, true},
		{3, nil, true},
		{10, nil, true},
		{11, ErrLocked, false},
		{12, ErrWrongPassword, false},
		{130, nil, true},
	}
	for _, c := range cases {
		f := newFake(t)
		f.on("unlock", 0, "", "Fatal: something\n", c.exit)
		err := f.r.Unlock(context.Background())
		if err == nil {
			t.Fatalf("exit %d: no error", c.exit)
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("exit %d: %v, want %v", c.exit, err, c.want)
		}
		if !strings.Contains(err.Error(), "Fatal: something") {
			t.Errorf("exit %d: error %q lacks restic's message", c.exit, err)
		}
		var e *Error
		if c.isError {
			if !errors.As(err, &e) || e.ExitCode != c.exit || !reflect.DeepEqual(e.Args, []string{"unlock"}) {
				t.Errorf("exit %d: %#v is not an *Error with that code", c.exit, err)
			}
		}
		if errors.Is(err, ErrLocked) && errors.Is(err, ErrWrongPassword) {
			t.Errorf("exit %d maps to both sentinel errors", c.exit)
		}
	}
}

func TestMissingBinary(t *testing.T) {
	r := &Restic{Bin: filepath.Join(t.TempDir(), "nope")}
	err := r.Init(context.Background())
	var e *Error
	if err == nil || errors.As(err, &e) || !strings.HasPrefix(err.Error(), "restic init:") {
		t.Fatalf("Init with a missing binary = %v", err)
	}
	if ok, err := r.Exists(context.Background()); ok || err == nil {
		t.Fatalf("Exists = %v, %v", ok, err)
	}
	if _, err := r.Backup(context.Background(), BackupOptions{}, "/x"); err == nil {
		t.Fatal("Backup succeeded")
	}
	if err := r.Ls(context.Background(), "s", "/", func(Node) {}); err == nil {
		t.Fatal("Ls succeeded")
	}
	if err := r.Dump(context.Background(), "s", "/f", &bytes.Buffer{}); err == nil {
		t.Fatal("Dump succeeded")
	}
}

func TestExists(t *testing.T) {
	cases := []struct {
		exit    int
		want    bool
		wantErr error
		anyErr  bool
	}{
		{0, true, nil, false},
		{10, false, nil, false},
		{12, false, ErrWrongPassword, true},
		{11, false, ErrLocked, true},
		{1, false, nil, true},
	}
	for _, c := range cases {
		f := newFake(t)
		f.on("cat", 0, "{}", "", c.exit)
		got, err := f.r.Exists(context.Background())
		if got != c.want || (err != nil) != c.anyErr || (c.wantErr != nil && !errors.Is(err, c.wantErr)) {
			t.Errorf("exit %d: Exists = %v, %v", c.exit, got, err)
		}
		if args := f.args(1); !reflect.DeepEqual(args, []string{"cat", "config"}) {
			t.Errorf("args = %q", args)
		}
	}
}

func TestEnsureRepository(t *testing.T) {
	ctx := context.Background()
	t.Run("exists", func(t *testing.T) {
		f := newFake(t)
		created, err := f.r.EnsureRepository(ctx)
		if created || err != nil || !reflect.DeepEqual(f.calls(), []string{"cat"}) {
			t.Fatalf("= %v, %v; calls %q", created, err, f.calls())
		}
	})
	t.Run("created", func(t *testing.T) {
		f := newFake(t)
		f.on("cat", 0, "", "Fatal: repository does not exist\n", 10)
		created, err := f.r.EnsureRepository(ctx)
		if !created || err != nil || !reflect.DeepEqual(f.calls(), []string{"cat", "init"}) {
			t.Fatalf("= %v, %v; calls %q", created, err, f.calls())
		}
	})
	t.Run("someone else created it meanwhile", func(t *testing.T) {
		f := newFake(t)
		f.on("cat", 1, "", "", 10)
		f.on("init", 0, "", "Fatal: config file already exists\n", 1)
		f.on("cat", 2, "{}", "", 0)
		created, err := f.r.EnsureRepository(ctx)
		if created || err != nil || !reflect.DeepEqual(f.calls(), []string{"cat", "init", "cat"}) {
			t.Fatalf("= %v, %v; calls %q", created, err, f.calls())
		}
	})
	t.Run("init fails", func(t *testing.T) {
		f := newFake(t)
		f.on("cat", 0, "", "", 10)
		f.on("init", 0, "", "Fatal: permission denied\n", 1)
		created, err := f.r.EnsureRepository(ctx)
		if created || err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("= %v, %v", created, err)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		f := newFake(t)
		f.on("cat", 0, "", "Fatal: wrong password\n", 12)
		created, err := f.r.EnsureRepository(ctx)
		if created || !errors.Is(err, ErrWrongPassword) || !reflect.DeepEqual(f.calls(), []string{"cat"}) {
			t.Fatalf("= %v, %v; calls %q", created, err, f.calls())
		}
	})
}

const backupOutput = `{"message_type":"status","percent_done":0.5}
not json at all
{"message_type":"error","error":{"message":"open /backup/filestore/ab/x: permission denied"},"during":"archival","item":"/backup/filestore/ab/x"}
{"message_type":"error","error":{"message":"something without an item"},"during":"scan"}
{"message_type":"summary","snapshot_id":"0123456789abcdef","files_new":3,"files_changed":1,"files_unmodified":7,` +
	`"data_added":1024,"data_added_packed":512,"total_files_processed":11,"total_bytes_processed":4096}
`

func TestBackup(t *testing.T) {
	f := newFake(t)
	f.on("backup", 0, backupOutput, "", 0)
	at := time.Date(2025, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	res, err := f.r.Backup(context.Background(), BackupOptions{
		Host: "ns/dep", Tags: []string{"deployment=ns/dep", "backup=b1"}, Excludes: []string{"*.tmp"}, Time: at,
	}, "/backup", "/other")
	if err != nil {
		t.Fatal(err)
	}
	want := Summary{SnapshotID: "0123456789abcdef", FilesNew: 3, FilesChanged: 1, FilesUnmodified: 7,
		DataAdded: 1024, DataAddedPacked: 512, TotalFilesProcessed: 11, TotalBytesProcessed: 4096}
	if res.Summary != want {
		t.Fatalf("Summary = %+v", res.Summary)
	}
	if !reflect.DeepEqual(res.Unreadable, []string{"/backup/filestore/ab/x"}) {
		t.Fatalf("Unreadable = %q", res.Unreadable)
	}
	wantArgs := []string{"backup", "--json", "--host", "ns/dep", "--tag", "deployment=ns/dep", "--tag", "backup=b1",
		"--exclude", "*.tmp", "--time", "2025-01-02 02:04:05", "/backup", "/other"}
	if got := f.args(1); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("args = %q\nwant %q", got, wantArgs)
	}
}

func TestBackupMinimalArgs(t *testing.T) {
	f := newFake(t)
	f.on("backup", 0, `{"message_type":"summary","snapshot_id":"abc"}`+"\n", "", 0)
	if _, err := f.r.Backup(context.Background(), BackupOptions{}, "/backup"); err != nil {
		t.Fatal(err)
	}
	if got := f.args(1); !reflect.DeepEqual(got, []string{"backup", "--json", "/backup"}) {
		t.Fatalf("args = %q", got)
	}
}

func TestBackupExitCodes(t *testing.T) {
	summary := `{"message_type":"summary","snapshot_id":"abc"}` + "\n"
	unreadable := `{"message_type":"error","item":"/backup/f"}` + "\n"
	cases := []struct {
		name       string
		stdout     string
		exit       int
		ok         bool
		wantErr    error
		unreadable int
	}{
		{"incomplete with snapshot", unreadable + summary, 3, true, nil, 1},
		{"incomplete without snapshot", unreadable, 3, false, nil, 1},
		{"no summary", "", 0, false, nil, 0},
		{"failed with summary", summary, 1, false, nil, 0},
		{"locked", "", 11, false, ErrLocked, 0},
		{"wrong password", "", 12, false, ErrWrongPassword, 0},
		{"no repository", "", 10, false, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.on("backup", 0, c.stdout, "Fatal: x\n", c.exit)
			res, err := f.r.Backup(context.Background(), BackupOptions{}, "/backup")
			if (err == nil) != c.ok {
				t.Fatalf("err = %v", err)
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if len(res.Unreadable) != c.unreadable {
				t.Fatalf("Unreadable = %q", res.Unreadable)
			}
		})
	}
}

func TestBackupHugeOutputLine(t *testing.T) {
	f := newFake(t)
	big := `{"message_type":"verbose_status","item":"` + strings.Repeat("x", 1<<20) + `"}` + "\n"
	f.on("backup", 0, big+`{"message_type":"summary","snapshot_id":"abc"}`+"\n", "", 0)
	res, err := f.r.Backup(context.Background(), BackupOptions{}, "/backup")
	if err != nil || res.Summary.SnapshotID != "abc" {
		t.Fatalf("= %+v, %v", res, err)
	}
}

const snapshotsJSON = `[
 {"id":"aaaa1111","short_id":"aaaa","time":"2025-01-01T00:00:00Z","hostname":"ns/dep","tags":["deployment=ns/dep","kind=manual"],"paths":["/backup"]},
 {"id":"bbbb2222","short_id":"bbbb","time":"2025-01-02T00:00:00.5+01:00","hostname":"ns/dep","paths":["/backup"]}
]`

func TestSnapshots(t *testing.T) {
	f := newFake(t)
	f.on("snapshots", 0, snapshotsJSON, "", 0)
	snaps, err := f.r.Snapshots(context.Background(), "ns/dep", "deployment=ns/dep", "kind=manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].ID != "aaaa1111" || snaps[0].ShortID != "aaaa" || snaps[0].Hostname != "ns/dep" ||
		!reflect.DeepEqual(snaps[0].Paths, []string{"/backup"}) || !snaps[0].Time.Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if !snaps[0].HasTag("kind=manual") || snaps[0].HasTag("kind=") || snaps[1].HasTag("kind=manual") {
		t.Fatal("HasTag")
	}
	want := []string{"snapshots", "--json", "--host", "ns/dep", "--tag", "deployment=ns/dep,kind=manual"}
	if got := f.args(1); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q", got)
	}

	if _, err := f.r.Snapshots(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := f.args(2); !reflect.DeepEqual(got, []string{"snapshots", "--json"}) {
		t.Fatalf("args without filters = %q", got)
	}
}

func TestSnapshotsErrors(t *testing.T) {
	f := newFake(t)
	f.on("snapshots", 1, "not json", "", 0)
	f.on("snapshots", 2, "", "Fatal: wrong password\n", 12)
	if _, err := f.r.Snapshots(context.Background(), "h"); err == nil {
		t.Fatal("bad JSON accepted")
	}
	if _, err := f.r.Snapshots(context.Background(), "h"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("err = %v", err)
	}
	// An empty repository prints [].
	f.on("snapshots", 3, "[]", "", 0)
	if snaps, err := f.r.Snapshots(context.Background(), "h"); err != nil || len(snaps) != 0 {
		t.Fatalf("= %v, %v", snaps, err)
	}
}

func TestSnapshot(t *testing.T) {
	one := `[{"id":"aaaa1111","short_id":"aaaa","paths":["/backup"]}]`
	cases := []struct {
		name, out string
		exit      int
		ok        bool
	}{
		{"one", one, 0, true},
		{"none", "[]", 0, false},
		{"two", snapshotsJSON, 0, false},
		{"bad json", "{", 0, false},
		{"restic fails", "", 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.on("snapshots", 0, c.out, "", c.exit)
			s, err := f.r.Snapshot(context.Background(), "aaaa")
			if (err == nil) != c.ok {
				t.Fatalf("err = %v", err)
			}
			if c.ok && s.ID != "aaaa1111" {
				t.Fatalf("snapshot = %+v", s)
			}
			if got := f.args(1); !reflect.DeepEqual(got, []string{"snapshots", "--json", "aaaa"}) {
				t.Fatalf("args = %q", got)
			}
		})
	}
}

func TestLs(t *testing.T) {
	f := newFake(t)
	out := `{"time":"2025-01-01T00:00:00Z","tree":"x","paths":["/backup"],"id":"aaaa","struct_type":"snapshot"}
{"name":"backup","type":"dir","path":"/backup","struct_type":"node"}
{"name":"dump.sql","type":"file","path":"/backup/dump.sql","size":42,"struct_type":"node"}
garbage
{"name":"abc","type":"file","path":"/backup/filestore/ab/abc","size":7,"message_type":"node"}
{"message_type":"summary","total_files":2}
`
	f.on("ls", 0, out, "", 0)
	var nodes []Node
	if err := f.r.Ls(context.Background(), "aaaa", "/backup", func(n Node) { nodes = append(nodes, n) }); err != nil {
		t.Fatal(err)
	}
	want := []Node{
		{Name: "backup", Type: "dir", Path: "/backup"},
		{Name: "dump.sql", Type: "file", Path: "/backup/dump.sql", Size: 42},
		{Name: "abc", Type: "file", Path: "/backup/filestore/ab/abc", Size: 7},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("nodes = %+v", nodes)
	}
	if got := f.args(1); !reflect.DeepEqual(got, []string{"ls", "--json", "--recursive", "aaaa", "/backup"}) {
		t.Fatalf("args = %q", got)
	}
}

func TestLsErrors(t *testing.T) {
	f := newFake(t)
	f.on("ls", 1, `{"name":"a","type":"file","path":"/a","struct_type":"node"}`+"\n", "Fatal: no snapshot\n", 1)
	var n int
	err := f.r.Ls(context.Background(), "x", "/", func(Node) { n++ })
	var e *Error
	if !errors.As(err, &e) || e.ExitCode != 1 {
		t.Fatalf("err = %v", err)
	}
	// A line longer than the scanner allows fails the listing.
	f.on("ls", 2, strings.Repeat("x", 17*1024*1024)+"\n", "", 0)
	if err := f.r.Ls(context.Background(), "x", "/", func(Node) {}); err == nil {
		t.Fatal("an over-long line was ignored")
	}
}

func TestDump(t *testing.T) {
	f := newFake(t)
	content := "line 1\nbinary \x00\xff\n"
	f.on("dump", 0, content, "", 0)
	var buf bytes.Buffer
	if err := f.r.Dump(context.Background(), "aaaa", "/backup/manifest.json", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != content {
		t.Fatalf("dumped %q", buf.String())
	}
	if got := f.args(1); !reflect.DeepEqual(got, []string{"dump", "aaaa", "/backup/manifest.json"}) {
		t.Fatalf("args = %q", got)
	}
	f.on("dump", 2, "", "Fatal: path not found\n", 1)
	if err := f.r.Dump(context.Background(), "aaaa", "/nope", &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "path not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreWithoutIncludes(t *testing.T) {
	f := newFake(t)
	if err := f.r.Restore(context.Background(), "aaaa", "/target"); err != nil {
		t.Fatal(err)
	}
	if got := f.args(1); !reflect.DeepEqual(got, []string{"restore", "aaaa", "--target", "/target"}) {
		t.Fatalf("args = %q", got)
	}
}

func TestRestoreIncludeFile(t *testing.T) {
	f := newFake(t)
	includes := []string{
		"/backup/filestore/ab/abc",
		"/backup/[a]*?.txt",
		`/backup/back\slash`,
		"/backup/$HOME/x",
		"#comment-looking",
		"!negation-looking",
		"/backup/mid#and!",
	}
	if err := f.r.Restore(context.Background(), "aaaa", "/target", includes...); err != nil {
		t.Fatal(err)
	}
	args := f.args(1)
	if len(args) != 6 || args[4] != "--include-file" {
		t.Fatalf("args = %q", args)
	}
	got := f.read("include.1")
	want := strings.Join([]string{
		"/backup/filestore/ab/abc",
		`/backup/\[a\]\*\?.txt`,
		`/backup/back\\slash`,
		"/backup/$$HOME/x",
		`\#comment-looking`,
		`\!negation-looking`,
		"/backup/mid#and!",
	}, "\n") + "\n"
	if got != want {
		t.Fatalf("include file:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(args[5]); !os.IsNotExist(err) {
		t.Fatalf("include file %s was left behind: %v", args[5], err)
	}
}

func TestRestoreRefusesUnmatchablePaths(t *testing.T) {
	for _, p := range []string{"/backup/a\nb", "/backup/a\rb", " /backup/a", "/backup/a ", "/backup/a\t"} {
		f := newFake(t)
		if err := f.r.Restore(context.Background(), "aaaa", "/target", "/ok", p); err == nil {
			t.Errorf("Restore accepted %q", p)
		}
		if len(f.calls()) != 0 {
			t.Errorf("restic ran for %q", p)
		}
	}
}

func TestRestoreFails(t *testing.T) {
	f := newFake(t)
	f.on("restore", 0, "", "Fatal: wrong password\n", 12)
	if err := f.r.Restore(context.Background(), "aaaa", "/t", "/x"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("err = %v", err)
	}
}

func TestEscapePattern(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"plain/path":  "plain/path",
		"*":           `\*`,
		"a?b":         `a\?b`,
		"[x]":         `\[x\]`,
		`\`:           `\\`,
		"$":           "$$",
		"${X}":        "$${X}",
		"#x#":         `\#x#`,
		"!x!":         `\!x!`,
		"é中😀*":        `é中😀\*`,
		"/a/**/b":     `/a/\*\*/b`,
		"x/#y":        "x/#y",
		"weird [a]*?": `weird \[a\]\*\?`,
	}
	for in, want := range cases {
		if got := escapePattern(in); got != want {
			t.Errorf("escapePattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContextCancelStopsRestic(t *testing.T) {
	f := newFake(t)
	f.write("out.unlock", "")
	// A restic that hangs.
	script := filepath.Join(f.dir, "restic")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := f.r.Unlock(ctx); err == nil {
		t.Fatal("a killed restic reported success")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel did not stop restic")
	}
}
