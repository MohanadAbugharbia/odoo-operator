package agent

// Tests of the Odoo zip writer, its upload and the deletion of zips whose
// backups retention forgot. They use the real rclone on local folders.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MohanadAbugharbia/odoo-operator/internal/backup/restic"
)

func hRealRclone(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone is not installed")
	}
	return bin
}

type hTarEntry struct {
	name, body string
	typ        byte
}

func hTar(t *testing.T, entries ...hTarEntry) *tar.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Size: int64(len(e.body)), ModTime: time.Unix(1700000000, 0)}
		if typ != tar.TypeReg {
			hdr.Size = 0
		}
		if typ == tar.TypeSymlink {
			hdr.Linkname = "/etc/passwd"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return tar.NewReader(&buf)
}

func hUnzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := out[f.Name]; dup {
			t.Fatalf("zip holds %s twice", f.Name)
		}
		out[f.Name] = string(b)
	}
	return out
}

func TestZipName(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 999, time.FixedZone("x", 3*3600))
	if got := ZipName("shop", at); got != "shop_2026-03-04_02-06-07.zip" {
		t.Fatalf("ZipName = %q (the time is in UTC)", got)
	}
}

func TestTarToOdooZip(t *testing.T) {
	root := "backup/"
	tr := hTar(t,
		hTarEntry{name: "backup", typ: tar.TypeDir},
		hTarEntry{name: "backup/dump.sql", body: "SELECT 1;"},
		hTarEntry{name: "backup/manifest.json", body: `{"db_name":"shop"}`},
		hTarEntry{name: "backup/filestore/ab", typ: tar.TypeDir},
		hTarEntry{name: "backup/filestore/ab/abc", body: "one"},
		hTarEntry{name: "backup/filestore-repaired/cd/cde", body: "two"},
		// A file both in filestore/ and filestore-repaired/ goes in once.
		hTarEntry{name: "backup/filestore-repaired/ab/abc", body: "one"},
		hTarEntry{name: "backup/filestore/ef/link", typ: tar.TypeSymlink},
		hTarEntry{name: "backup/state.json", body: "not part of an Odoo zip"},
	)
	var out bytes.Buffer
	files, err := tarToOdooZip(tr, root, &out)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Fatalf("files = %d, want 2", files)
	}
	got := hUnzip(t, out.Bytes())
	want := map[string]string{
		"dump.sql": "SELECT 1;", "manifest.json": `{"db_name":"shop"}`,
		"filestore/ab/abc": "one", "filestore/cd/cde": "two",
	}
	if d := diffMaps(want, got); d != "" {
		t.Fatal(d)
	}
}

func TestTarToOdooZipRefuses(t *testing.T) {
	cases := map[string][]hTarEntry{
		"no dump":       {{name: "backup/manifest.json", body: "{}"}},
		"no manifest":   {{name: "backup/dump.sql", body: "x"}},
		"outside root":  {{name: "backup/dump.sql", body: "x"}, {name: "other/manifest.json", body: "{}"}},
		"empty archive": nil,
	}
	want := map[string]string{
		"no dump": "no dump.sql", "no manifest": "no manifest.json", "outside root": "outside", "empty archive": "no dump.sql",
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tarToOdooZip(hTar(t, entries...), "backup/", io.Discard)
			if err == nil || !strings.Contains(err.Error(), want[name]) {
				t.Fatalf("err = %v, want %q", err, want[name])
			}
		})
	}
}

func TestUploadZip(t *testing.T) {
	bin := hRealRclone(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "ns", "shop", "shop_x.zip")
	n, err := uploadZip(context.Background(), bin, target, func(w io.Writer) (int, error) {
		_, err := w.Write([]byte("zip"))
		return 7, err
	})
	if err != nil || n != 7 {
		t.Fatalf("upload = %d, %v", n, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "zip" {
		t.Fatalf("content %q", b)
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 1 {
		t.Fatalf("folder holds %v", entries)
	}

	// A zip that fails while being written leaves nothing behind, not even
	// a partial file.
	broken := filepath.Join(dir, "ns", "shop", "shop_y.zip")
	_, err = uploadZip(context.Background(), bin, broken, func(w io.Writer) (int, error) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1<<20))
		return 0, io.ErrUnexpectedEOF
	})
	if err == nil {
		t.Fatal("a failed write uploaded")
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 || entries[0].Name() != "shop_x.zip" {
		t.Fatalf("folder holds %v after a failed upload", entries)
	}
}

// deleteZips deletes the zips of forgotten snapshots and nothing else,
// whatever else the folder holds.
func TestDeleteZipsOnlyOwnNames(t *testing.T) {
	bin := hRealRclone(t)
	rng := rand.New(rand.NewSource(1))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for round := 0; round < 20; round++ {
		root := t.TempDir()
		folder := "ns/shop"
		dir := filepath.Join(root, folder)
		var forgotten []restic.Snapshot
		var keep []string
		for i := 0; i < 1+rng.Intn(8); i++ {
			at := base.Add(time.Duration(rng.Intn(1_000_000)) * time.Second)
			name := ZipName("shop", at)
			if rng.Intn(2) == 0 {
				forgotten = append(forgotten, restic.Snapshot{Time: at})
				hWrite(t, filepath.Join(dir, name), []byte("old"))
			} else if !slices.ContainsFunc(forgotten, func(s restic.Snapshot) bool { return ZipName("shop", s.Time) == name }) {
				keep = append(keep, name)
				hWrite(t, filepath.Join(dir, name), []byte("kept"))
			}
		}
		// Look-alikes this instance's backups never get, and other folders.
		others := make([]string, 0, 4*len(forgotten)+2)
		for _, s := range forgotten {
			n := ZipName("shop", s.Time)
			others = append(others, n+".bak", "x"+n, strings.Replace(n, "shop", "other", 1), "sub/"+n)
		}
		others = append(others, "notes.txt", ".shop_2026-01-01_00-00-00.zip.partial.bak", ".shop_x.zip.partial",
			".other_2026-01-01_00-00-00.zip.partial", "shop_2026-01-01_00-00-00.zip.partial", ".shop_2026-01-01_00-00.zip.partial")
		for _, o := range others {
			hWrite(t, filepath.Join(dir, o), []byte("other"))
		}
		// What killed uploads of this instance's zips left.
		partials := []string{"." + ZipName("shop", base) + ".partial", "." + ZipName("shop", base.Add(time.Hour)) + ".partial"}
		for _, p := range partials {
			hWrite(t, filepath.Join(dir, p), []byte("cut short"))
		}
		hWrite(t, filepath.Join(root, "ns", "other", "."+ZipName("shop", base)+".partial"), []byte("other"))
		hWrite(t, filepath.Join(root, "ns", "other", "shop_2026-01-01_00-00-00.zip"), []byte("other"))

		e := &Env{Config: hUnreachable()}
		e.Config.Name = "shop"
		if err := deleteZips(context.Background(), e, CopyTarget{ZipTarget: root, Rclone: bin}, folder, forgotten, ""); err != nil {
			t.Fatal(err)
		}
		for _, s := range forgotten {
			if _, err := os.Stat(filepath.Join(dir, ZipName("shop", s.Time))); !os.IsNotExist(err) {
				t.Fatalf("round %d: %s survived", round, ZipName("shop", s.Time))
			}
		}
		for _, p := range partials {
			if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
				t.Fatalf("round %d: the partial upload %s survived", round, p)
			}
		}
		for _, n := range append(keep, others...) {
			if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
				t.Fatalf("round %d: %s was deleted", round, n)
			}
		}
		for _, n := range []string{"shop_2026-01-01_00-00-00.zip", "." + ZipName("shop", base) + ".partial"} {
			if _, err := os.Stat(filepath.Join(root, "ns", "other", n)); err != nil {
				t.Fatalf("another instance's %s was deleted", n)
			}
		}
	}
}

// With nothing forgotten deleteZips only looks for partial uploads, and
// failing to list the folder is no error then.
func TestDeleteZipsNothingForgotten(t *testing.T) {
	e := &Env{Config: hUnreachable()}
	if err := deleteZips(context.Background(), e, CopyTarget{ZipTarget: "/nonexistent", Rclone: "/nonexistent/rclone"}, "ns/shop", nil, ""); err != nil {
		t.Fatal(err)
	}
	// Nor when the only forgotten zip is the one just written.
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := deleteZips(context.Background(), e, CopyTarget{ZipTarget: "/nonexistent", Rclone: "/nonexistent/rclone"}, "ns/shop",
		[]restic.Snapshot{{Time: at}}, ZipName(e.Config.Name, at)); err != nil {
		t.Fatal(err)
	}
}

// Partial uploads go even when retention forgot nothing.
func TestDeleteZipsPartialsOnly(t *testing.T) {
	bin := hRealRclone(t)
	root := t.TempDir()
	dir := filepath.Join(root, "ns", "shop")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	partial, kept := "."+ZipName("shop", at)+".partial", ZipName("shop", at.Add(time.Hour))
	hWrite(t, filepath.Join(dir, partial), []byte("cut short"))
	hWrite(t, filepath.Join(dir, kept), []byte("zip"))
	e := &Env{Config: hUnreachable()}
	e.Config.Name = "shop"
	if err := deleteZips(context.Background(), e, CopyTarget{ZipTarget: root, Rclone: bin}, "ns/shop", nil, kept); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, partial)); !os.IsNotExist(err) {
		t.Fatal("the partial upload survived")
	}
	if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
		t.Fatal("the zip just written was deleted")
	}
}

func TestIsPartialZip(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if !isPartialZip("shop", "."+ZipName("shop", at)+".partial") {
		t.Fatal("a partial upload of shop is not recognised")
	}
	for _, name := range []string{
		ZipName("shop", at), "." + ZipName("shop", at), ZipName("shop", at) + ".partial",
		"." + ZipName("other", at) + ".partial", "." + ZipName("shop_x", at) + ".partial",
		".shop_2026-03-04.zip.partial", ".shop_2026-03-04_05-06-07x.zip.partial", ".shop_.zip.partial", "",
	} {
		if isPartialZip("shop", name) {
			t.Errorf("%q is taken for a partial upload of shop", name)
		}
	}
}

func TestCopyTargetsFromEnvZip(t *testing.T) {
	t.Setenv("COPY1_ZIP_TARGET", "/store/zips")
	t.Setenv("COPY2_ZIP_TARGET", "")
	t.Setenv("COPY2_RESTIC_REPOSITORY", "/store/repo")
	t.Setenv("COPY2_RESTIC_PASSWORD_FILE", "/pw")
	got, err := CopyTargetsFromEnv(&restic.Restic{Bin: "restic"}, []string{"zips", "offsite"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ZipTarget != "/store/zips" || got[0].Restic != nil || got[0].Store != "zips" {
		t.Fatalf("zip target = %+v", got[0])
	}
	if got[1].ZipTarget != "" || got[1].Restic == nil || !slices.Contains(got[1].Restic.Env, "RESTIC_REPOSITORY=/store/repo") {
		t.Fatalf("restic target = %+v", got[1])
	}
}
