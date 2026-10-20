package agent

import (
	"bytes"
	"io"
	"strings"
)

// sessionSettings are settings newer pg_dumps put in a dump's preamble, with
// the server major that first knows them. An older server refuses to SET one,
// which would stop the load, and each is safe to leave out (a dump only sets
// it to its default).
var sessionSettings = map[string]int{
	"transaction_timeout": 17,
}

// preambleFilter passes a SQL dump through, leaving out the SET lines of its
// preamble that the server does not know. It looks only at the preamble
// (comments, SET, set_config and psql meta-commands before the first
// statement), then passes everything through untouched.
type preambleFilter struct {
	w           io.Writer
	serverMajor int
	line        []byte
	done        bool
}

func (f *preambleFilter) Write(p []byte) (int, error) {
	n := len(p)
	for !f.done && len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			f.line = append(f.line, p...)
			return n, nil
		}
		f.line = append(f.line, p[:i+1]...)
		p = p[i+1:]
		if err := f.emit(); err != nil {
			return 0, err
		}
	}
	if len(p) > 0 {
		if _, err := f.w.Write(p); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// emit writes (or leaves out) the buffered line and decides whether the
// preamble is over.
func (f *preambleFilter) emit() error {
	line := f.line
	f.line = f.line[:0]
	text := strings.TrimSpace(string(line))
	switch {
	case text == "", strings.HasPrefix(text, "--"), strings.HasPrefix(text, `\`),
		strings.HasPrefix(text, "SELECT pg_catalog.set_config("):
	case strings.HasPrefix(text, "SET "):
		name, _, _ := strings.Cut(strings.TrimPrefix(text, "SET "), " ")
		if since, ok := sessionSettings[name]; ok && f.serverMajor > 0 && f.serverMajor < since {
			return nil
		}
	default:
		f.done = true
	}
	_, err := f.w.Write(line)
	return err
}

// flush writes a last line that had no newline.
func (f *preambleFilter) flush() error {
	if len(f.line) == 0 {
		return nil
	}
	_, err := f.w.Write(f.line)
	f.line = nil
	return err
}
