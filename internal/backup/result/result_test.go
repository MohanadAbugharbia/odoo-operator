package result

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFailed(t *testing.T) {
	r := Failed(StepDump, errors.New("pg_dump: boom"))
	if r.OK || r.Step != StepDump || r.Error != "pg_dump: boom" {
		t.Fatalf("Failed = %+v", r)
	}
	if r.Deferred || r.LiveChanged || r.PreviousKept || r.SnapshotID != "" {
		t.Fatalf("Failed set more than step and error: %+v", r)
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		r    Result
		want int
	}{
		{Result{OK: true}, 0},
		{Result{OK: true, Deferred: true}, 0},
		{Result{Step: StepLoad, Error: "x"}, ExitFailed},
		{Result{Step: StepCheck, Error: "x", Final: true}, ExitFinal},
	}
	for _, c := range cases {
		if got := ExitCode(c.r); got != c.want {
			t.Errorf("ExitCode(%+v) = %d, want %d", c.r, got, c.want)
		}
	}
	if ExitFailed == ExitFinal || ExitUsage == ExitFinal || ExitUsage == ExitFailed {
		t.Error("the exit codes must differ")
	}
}

func TestEncodeSmallIsPlainJSON(t *testing.T) {
	r := Result{
		OK: true, SnapshotID: "abc123", OdooVersion: "18.0", PgServerVersion: "16.4",
		DatabaseBytes: 1 << 40, FilestoreBytes: 7, FilestoreFiles: 3, AddedBytes: 9,
		RepairedFiles: 1, MissingFiles: 2, MissingNames: []string{"ab/abc", "cd/cde"},
		Warnings: []string{"w1", "w2"}, Image: "odoo:18", UpgradeToken: "t",
		MissingModules: []string{"sale"}, LiveChanged: true, PreviousKept: true, Deferred: true,
	}
	want, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got := Encode(r)
	if string(got) != string(want) {
		t.Fatalf("Encode changed a result that fits:\n got %s\nwant %s", got, want)
	}
	back, err := Decode(string(got))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, r) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", back, r)
	}
}

func TestEncodeOmitsEmpty(t *testing.T) {
	if got := string(Encode(Result{OK: true})); got != `{"ok":true}` {
		t.Fatalf("Encode(ok) = %s", got)
	}
	if got := string(Encode(Result{})); got != `{"ok":false}` {
		t.Fatalf("Encode(zero) = %s", got)
	}
}

func names(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%02x/%040x", prefix, i%256, i)
	}
	return out
}

func checkEncoded(t *testing.T, r Result) Result {
	t.Helper()
	b := Encode(r)
	if len(b) > MaxBytes {
		t.Fatalf("Encode returned %d bytes, more than %d", len(b), MaxBytes)
	}
	if !json.Valid(b) {
		t.Fatalf("Encode returned invalid JSON: %q", b)
	}
	back, err := Decode(string(b))
	if err != nil {
		t.Fatalf("Decode(Encode(r)): %v", err)
	}
	if back.OK != r.OK || back.Deferred != r.Deferred || back.LiveChanged != r.LiveChanged ||
		back.PreviousKept != r.PreviousKept || back.Final != r.Final {
		t.Fatalf("flags lost: got %+v from %+v", back, r)
	}
	return back
}

func TestEncodeTrimsLongLists(t *testing.T) {
	cases := map[string]Result{
		"missing names": {OK: true, SnapshotID: "s", MissingFiles: 5000, MissingNames: names(5000, "")},
		"warnings":      {OK: true, Warnings: names(1000, "warning ")},
		"modules":       {OK: true, MissingModules: names(1000, "module_")},
		"everything": {OK: false, Step: StepCheck, Error: strings.Repeat("e", 100000), MissingNames: names(3000, ""),
			Warnings: names(3000, "w"), MissingModules: names(3000, "m"), LiveChanged: true},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			back := checkEncoded(t, r)
			if back.MissingFiles != r.MissingFiles {
				t.Fatalf("MissingFiles = %d, want the full count %d", back.MissingFiles, r.MissingFiles)
			}
			if len(r.Warnings) > 1 && len(back.Warnings) > 0 && back.Warnings[len(back.Warnings)-1] != moreWarnings {
				t.Fatalf("trimmed warnings do not say more are in the log: %q", back.Warnings)
			}
		})
	}
}

func TestEncodeKeepsSnapshotIDWhenTrimming(t *testing.T) {
	back := checkEncoded(t, Result{OK: true, SnapshotID: "0123456789abcdef", MissingNames: names(10000, "")})
	if back.SnapshotID != "0123456789abcdef" {
		t.Fatalf("SnapshotID = %q", back.SnapshotID)
	}
	if len(back.MissingNames) == 0 {
		t.Fatal("trimmed every missing name although some fit")
	}
}

func TestEncodeTrimsLongError(t *testing.T) {
	back := checkEncoded(t, Result{Step: StepLoad, Error: strings.Repeat("x", 10000)})
	if back.Step != StepLoad {
		t.Fatalf("Step = %q", back.Step)
	}
	if !strings.HasPrefix(back.Error, "xxxx") || !strings.HasSuffix(back.Error, "(see the Job log)") {
		t.Fatalf("Error = %q", back.Error)
	}
}

// The cases that used to loop forever: a trim that does not make the result
// smaller must not be applied again.
func TestEncodeTerminatesOnStubbornFields(t *testing.T) {
	cases := map[string]Result{
		"long error and one huge warning":   {Error: strings.Repeat("e", 1000), Warnings: []string{strings.Repeat("w", 5000)}},
		"two warnings, the first huge":      {Warnings: []string{strings.Repeat("w", 5000), "x"}},
		"three warnings, the first huge":    {Warnings: []string{strings.Repeat("w", 5000), "x", "y"}},
		"huge step":                         {Step: strings.Repeat("s", 5000)},
		"final, huge error and step":        {Final: true, Step: strings.Repeat("s", 5000), Error: strings.Repeat("e", 9000)},
		"huge snapshot id":                  {OK: true, SnapshotID: strings.Repeat("a", 5000)},
		"huge image and token":              {OK: true, Image: strings.Repeat("i", 3000), UpgradeToken: strings.Repeat("u", 3000)},
		"escaped characters everywhere":     {Step: strings.Repeat("<", 900), Error: strings.Repeat("&", 900), SnapshotID: strings.Repeat(">", 900)},
		"invalid utf-8":                     {Error: strings.Repeat("\xff", 9000), Warnings: []string{strings.Repeat("\xfe", 9000)}},
		"huge warning already marked":       {Warnings: []string{strings.Repeat("w", 9000), moreWarnings}},
		"long error plus huge module names": {Error: strings.Repeat("e", 600), MissingModules: []string{strings.Repeat("m", 9000)}},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) { checkEncoded(t, r) })
	}
}

func TestEncodeDoesNotChangeTheCallersSlices(t *testing.T) {
	warnings := []string{strings.Repeat("w", 9000)}
	missing := names(5000, "")
	before := append([]string(nil), missing...)
	Encode(Result{Warnings: warnings, MissingNames: missing})
	if len(warnings[0]) != 9000 {
		t.Fatalf("Encode shortened the caller's warning to %d bytes", len(warnings[0]))
	}
	if !reflect.DeepEqual(missing, before) {
		t.Fatal("Encode changed the caller's missing names")
	}
}

func randString(rng *rand.Rand, n int) string {
	alphabet := []rune("abcdefXYZ0123/._-<>&\"\\\n\t é中😀 ")
	var b strings.Builder
	for i := 0; i < n; i++ {
		if rng.Intn(50) == 0 {
			b.WriteByte(byte(0x80 + rng.Intn(0x7f))) // a stray invalid byte
			continue
		}
		b.WriteRune(alphabet[rng.Intn(len(alphabet))])
	}
	return b.String()
}

func randList(rng *rand.Rand) []string {
	if rng.Intn(3) == 0 {
		return nil
	}
	n := rng.Intn(400)
	if rng.Intn(10) == 0 {
		n = rng.Intn(5000)
	}
	out := make([]string, n)
	for i := range out {
		// Mostly short entries, as in real lists, with an occasional huge one.
		size := rng.Intn(80)
		if rng.Intn(200) == 0 {
			size = randSize(rng)
		}
		out[i] = randString(rng, size)
	}
	return out
}

func randSize(rng *rand.Rand) int {
	switch rng.Intn(4) {
	case 0:
		return 0
	case 1:
		return rng.Intn(64)
	case 2:
		return rng.Intn(1024)
	}
	return rng.Intn(20000)
}

func TestEncodeProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	iterations := 600
	if testing.Short() {
		iterations = 100
	}
	for i := 0; i < iterations; i++ {
		r := Result{
			OK:              rng.Intn(2) == 0,
			Step:            randString(rng, randSize(rng)/4),
			Error:           randString(rng, randSize(rng)),
			Deferred:        rng.Intn(2) == 0,
			Warnings:        randList(rng),
			SnapshotID:      randString(rng, randSize(rng)/8),
			OdooVersion:     randString(rng, randSize(rng)/8),
			PgServerVersion: randString(rng, randSize(rng)/8),
			DatabaseBytes:   rng.Int63(),
			MissingNames:    randList(rng),
			Image:           randString(rng, randSize(rng)/4),
			UpgradeToken:    randString(rng, randSize(rng)/4),
			MissingModules:  randList(rng),
			LiveChanged:     rng.Intn(2) == 0,
			PreviousKept:    rng.Intn(2) == 0,
			Final:           rng.Intn(2) == 0,
		}
		b := Encode(r)
		if len(b) > MaxBytes || !json.Valid(b) {
			t.Fatalf("iteration %d: %d bytes, valid=%v", i, len(b), json.Valid(b))
		}
		back, err := Decode(string(b))
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if back.OK != r.OK || back.LiveChanged != r.LiveChanged || back.PreviousKept != r.PreviousKept || back.Deferred != r.Deferred ||
			back.Final != r.Final {
			t.Fatalf("iteration %d: flags lost", i)
		}
		if full, _ := json.Marshal(r); len(full) <= MaxBytes && string(full) != string(b) {
			t.Fatalf("iteration %d: a result that fits was changed", i)
		}
	}
}

func TestDecode(t *testing.T) {
	for _, msg := range []string{"", "   ", "\n\t"} {
		if _, err := Decode(msg); !errors.Is(err, ErrNoResult) {
			t.Errorf("Decode(%q) = %v, want ErrNoResult", msg, err)
		}
	}
	for _, msg := range []string{"garbage", "{", `{"ok":"yes"}`, "[1,2]", "Error: container killed"} {
		_, err := Decode(msg)
		if err == nil || errors.Is(err, ErrNoResult) {
			t.Errorf("Decode(%q) = %v, want a parse error", msg, err)
		}
	}
	r, err := Decode("  \n" + `{"ok":true,"snapshotID":"x","unknownKey":1}` + "\n")
	if err != nil || !r.OK || r.SnapshotID != "x" {
		t.Fatalf("Decode with whitespace and unknown keys = %+v, %v", r, err)
	}
}

func TestWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	r := Result{OK: true, SnapshotID: "abc", MissingNames: names(5000, "")}
	if err := Write(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(Encode(r)) {
		t.Fatal("Write did not write Encode's output")
	}
	if len(data) > MaxBytes {
		t.Fatalf("wrote %d bytes", len(data))
	}
	// Overwrites a previous attempt's result.
	if err := Write(path, Result{OK: true}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"ok":true}` {
		t.Fatalf("after overwrite: %s", data)
	}
	if err := Write(filepath.Join(t.TempDir(), "missing", "dir", "log"), r); err == nil {
		t.Fatal("Write into a missing folder succeeded")
	}
}

func TestTruncate(t *testing.T) {
	if truncate("abc", 3) != "abc" || truncate("abcd", 3) != "abc…" || truncate("", 0) != "" {
		t.Fatal("truncate")
	}
}
