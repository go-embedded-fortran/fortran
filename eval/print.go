package eval

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// An edit descriptor of the formats the prototype knows.
type edit struct {
	kind byte // 'a', 'f', 'e' (es)
	w, d int
}

// parseFormat reads a character format such as "(es24.16)" or "(a,f0.6)": a
// list of the a, fW.D and esW.D descriptors.
func parseFormat(s string) ([]edit, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return nil, fmt.Errorf("format %q is not a parenthesised list", s)
	}
	var out []edit
	for _, d := range strings.Split(s[1:len(s)-1], ",") {
		d = strings.ToLower(strings.TrimSpace(d))
		var e edit
		var rest string
		switch {
		case d == "a":
			out = append(out, edit{kind: 'a'})
			continue
		case strings.HasPrefix(d, "es"):
			e.kind, rest = 'e', d[2:]
		case strings.HasPrefix(d, "f"):
			e.kind, rest = 'f', d[1:]
		default:
			return nil, fmt.Errorf("edit descriptor %q is not supported", d)
		}
		ws, ds, ok := strings.Cut(rest, ".")
		w, err1 := strconv.Atoi(ws)
		dd, err2 := strconv.Atoi(ds)
		if !ok || err1 != nil || err2 != nil || w < 0 || dd < 0 || w > 1<<16 || dd > 1<<10 {
			return nil, fmt.Errorf("edit descriptor %q is malformed", d)
		}
		e.w, e.d = w, dd
		out = append(out, e)
	}
	return out, nil
}

func (e edit) real(v float64) string {
	var s string
	if e.kind == 'e' {
		s = strconv.FormatFloat(v, 'E', e.d, 64)
	} else {
		s = strconv.FormatFloat(v, 'f', e.d, 64)
	}
	if len(s) < e.w {
		s = strings.Repeat(" ", e.w-len(s)) + s
	}
	return s
}

func (c *compiler) print(s *syntax.IOStmt) (stmtFn, error) {
	if s.Kind != "print" {
		return nil, c.errf(s, "only print is supported")
	}
	lit, ok := s.Format.(*syntax.StringLit)
	if !ok {
		return nil, c.errf(s, "only a character-literal format is supported")
	}
	eds, err := parseFormat(lit.Value)
	if err != nil {
		return nil, c.errf(s, "%v", err)
	}
	if len(eds) != len(s.Items) {
		return nil, c.errf(s, "%d edit descriptors for %d items (format reversion is not supported)", len(eds), len(s.Items))
	}
	parts := make([]func(*frame) string, len(eds))
	for i, it := range s.Items {
		e := eds[i]
		if sl, ok := it.(*syntax.StringLit); ok && e.kind == 'a' {
			v := sl.Value
			parts[i] = func(*frame) string { return v }
			continue
		}
		if e.kind == 'a' {
			return nil, c.errf(it, "the a descriptor needs a character literal")
		}
		x, err := c.expr(it)
		if err != nil {
			return nil, err
		}
		if x.k != kReal {
			return nil, c.errf(it, "the f and es descriptors need a real(8) value")
		}
		r := x.r
		parts[i] = func(f *frame) string { return e.real(r(f)) }
	}
	return func(f *frame) {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p(f))
		}
		b.WriteByte('\n')
		fmt.Fprint(f.out, b.String())
	}, nil
}
