package restic

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeScript stands in for restic. For every call it records the arguments
// (args.<n>, one per line), the RESTIC_* environment (env.<n>) and a copy of
// any --include-file (include.<n>), and appends the subcommand to calls.
// What it prints and how it exits come from files in its folder:
// out.<sub>, err.<sub> and exit.<sub>, or out.<sub>.<k> etc. for the k-th
// call of that subcommand.
const fakeScript = `#!/bin/sh
dir="$FAKE_DIR"
n=$(cat "$dir/count" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$dir/count"
skip=0
sub=""
prev=""
: > "$dir/args.$n"
for a in "$@"; do
  printf '%s\n' "$a" >> "$dir/args.$n"
  if [ "$prev" = "--include-file" ]; then cp "$a" "$dir/include.$n"; fi
  if [ -z "$sub" ]; then
    if [ "$skip" = 1 ]; then skip=0
    elif [ "$a" = "-o" ] || [ "$a" = "--retry-lock" ]; then skip=1
    elif [ "$a" = "--no-lock" ]; then :
    else sub="$a"; fi
  fi
  prev="$a"
done
echo "$sub" >> "$dir/calls"
k=$(grep -cx "$sub" "$dir/calls")
env | grep -E '^(RESTIC_|FAKE_MARK)' | sort > "$dir/env.$n"
pick() {
  if [ -e "$dir/$1.$sub.$k" ]; then echo "$dir/$1.$sub.$k"
  elif [ -e "$dir/$1.$sub" ]; then echo "$dir/$1.$sub"; fi
}
o=$(pick out); if [ -n "$o" ]; then cat "$o"; fi
e=$(pick err); if [ -n "$e" ]; then cat "$e" >&2; fi
x=$(pick exit); code=0; if [ -n "$x" ]; then code=$(cat "$x"); fi
exit "$code"
`

type fake struct {
	t   *testing.T
	dir string
	r   *Restic
}

func newFake(t *testing.T) *fake {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	if err := os.WriteFile(bin, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fake{t: t, dir: dir, r: &Restic{Bin: bin, Env: []string{"FAKE_DIR=" + dir}}}
}

func (f *fake) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// on sets what the sub command prints and its exit code; call > 0 limits it
// to that call of the sub command.
func (f *fake) on(sub string, call int, stdout, stderr string, exit int) {
	suffix := sub
	if call > 0 {
		suffix += "." + strconv.Itoa(call)
	}
	f.write("out."+suffix, stdout)
	f.write("err."+suffix, stderr)
	f.write("exit."+suffix, strconv.Itoa(exit))
}

func (f *fake) read(name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *fake) calls() []string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Fields(string(data))
}

// args returns the arguments of the n-th call (1-based).
func (f *fake) args(n int) []string {
	f.t.Helper()
	s := strings.TrimSuffix(f.read("args."+strconv.Itoa(n)), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
