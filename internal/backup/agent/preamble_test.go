package agent

import (
	"bytes"
	"testing"
)

func TestPreambleFilter(t *testing.T) {
	dump := "\\restrict abc\n--\n-- PostgreSQL database dump\n--\n\nSET statement_timeout = 0;\n" +
		"SET transaction_timeout = 0;\nSELECT pg_catalog.set_config('search_path', '', false);\n" +
		"CREATE TABLE t (a text);\nSET transaction_timeout = 0;\nCOPY t (a) FROM stdin;\nx"
	without := "\\restrict abc\n--\n-- PostgreSQL database dump\n--\n\nSET statement_timeout = 0;\n" +
		"SELECT pg_catalog.set_config('search_path', '', false);\n" +
		"CREATE TABLE t (a text);\nSET transaction_timeout = 0;\nCOPY t (a) FROM stdin;\nx"
	for _, cs := range []struct {
		major int
		want  string
	}{{16, without}, {17, dump}, {18, dump}} {
		// Write it in small pieces, so lines cross Write calls.
		var out bytes.Buffer
		f := &preambleFilter{w: &out, serverMajor: cs.major}
		for i := 0; i < len(dump); i += 7 {
			end := min(i+7, len(dump))
			if n, err := f.Write([]byte(dump[i:end])); err != nil || n != end-i {
				t.Fatalf("Write = %d, %v", n, err)
			}
		}
		if err := f.flush(); err != nil {
			t.Fatal(err)
		}
		if out.String() != cs.want {
			t.Errorf("server %d:\n%s\nwant:\n%s", cs.major, out.String(), cs.want)
		}
	}
}
