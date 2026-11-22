package eval

import (
	"slices"

	"github.com/go-embedded-fortran/fortran/syntax"
)

// bitSize is BIT_SIZE of an integer of kind kd (0 is the default kind 8 of an
// ex; see ex.kd).
func bitSize(kd int) int64 {
	switch kd {
	case 1:
		return 8
	case 2:
		return 16
	case 4:
		return 32
	}
	return 64
}

// mvbits compiles CALL MVBITS(FROM, FROMPOS, LEN, TO, TOPOS) for scalar
// integers (F2018 16.9.137): LEN bits of FROM from FROMPOS on are copied into
// TO from TOPOS on, and the other bits of TO are kept. TO is a variable; it is
// read and written at one place, fixed before FROM and the positions are read
// (they may be TO itself: the value read is the one before the call).
func (c *compiler) mvbits(s *syntax.Call) (stmtFn, error) {
	formals := []string{"from", "frompos", "len", "to", "topos"}
	got := map[string]syntax.Expr{}
	pos := true
	for i, a := range s.Args {
		name := a.Keyword
		if name != "" {
			pos = false
		} else if pos && i < len(formals) {
			name = formals[i]
		}
		if !slices.Contains(formals, name) || a.Value == nil || got[name] != nil {
			return nil, c.errf(s, "mvbits takes the arguments from, frompos, len, to and topos")
		}
		got[name] = a.Value
	}
	vals := make([]*ex, len(formals))
	for i, name := range formals {
		a := got[name]
		if a == nil {
			return nil, c.errf(s, "mvbits: argument %s is missing", name)
		}
		v, err := c.expr(a)
		if err == nil && v.k != kInt {
			err = c.errf(a, "mvbits: argument %s must be a scalar integer (arrays are not supported yet)", name)
		}
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	from, fpos, n, to, tpos := vals[0].i, vals[1].i, vals[2].i, vals[3], vals[4].i
	// FROM and TO are of one kind (gfortran refuses otherwise), and TO, a
	// variable, gives it: the kind of an expression such as n+1 is not kept
	kd := to.kd
	w := bitSize(kd)
	// moved is TO with the bits moved in, from the value old it had
	moved := func(f *frame, old int64) int64 {
		src, fp, ln, tp := from(f), fpos(f), n(f), tpos(f)
		if fp < 0 || ln < 0 || tp < 0 || fp+ln > w || tp+ln > w {
			fail(f, "mvbits: frompos=%d len=%d topos=%d do not fit in %d bits", fp, ln, tp, w)
		}
		// LEN 0 makes m 0 (a Go shift by 64 is 0): TO is kept
		m := ^uint64(0) >> uint(64-ln)
		bits := (uint64(src) >> uint(fp)) & m
		return narrow(kd, int64(uint64(old)&^(m<<uint(tp))|bits<<uint(tp)))
	}
	// TO is a variable (a name, an element or a component) with a place: (j),
	// j+1 and a named constant are values
	_, isName := got["to"].(*syntax.Ident)
	if to.pl == nil || !isName && !c.isDesignator(got["to"]) {
		return nil, c.errf(got["to"], "mvbits: to must be a variable (a pointer is not supported yet)")
	}
	pl := to.pl
	return func(f *frame) {
		r := pl(f)
		r.setInt(moved(f, r.getInt()))
	}, nil
}
