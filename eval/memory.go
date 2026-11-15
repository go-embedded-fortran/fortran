package eval

import (
	"io"
	"math"
)

// The storage a program holds is counted, not measured: every variable, array,
// derived-type object and character component is charged to a counter when it
// is made, before it is allocated, and credited back when it is let go. A
// program that would hold more than SessionOptions.MaxAlloc bytes is refused
// with an error, and no allocation is made for it, whatever its size: many
// small objects count as much as one big array.
//
// What the counter holds is the session's own storage (the process may hold
// more: other runtimes in the same tab have their own bounds), and it does not
// depend on the collector or on the host: a count is the same on every machine.
//
//   - A charge has an owner, the frame the storage lives in: a variable of a
//     procedure is credited when the procedure returns, a module's variable
//     never (a module's frame lives as long as the session). The owner of an
//     allocatable array is the frame that declares it, not the one that
//     executes the ALLOCATE.
//   - DEALLOCATE credits the array, and so does the reassignment of an
//     allocatable array that takes a new shape.
//   - A temporary (the value of an array expression, a section, a constructor)
//     is checked against what is left but not retained: it is garbage at the end
//     of its statement.
//   - A cell that fails leaves the frames of its procedures unwound without
//     their credit, so the counter of the next cell starts from what the
//     session's module frames hold, which is exact.

// Sizes the counter charges for what the evaluator allocates besides the data:
// the header of an array, an object, a slot of a derived-type object.
const (
	arrayOverhead  = 96
	objectOverhead = 48
	slotBytes      = 24
)

// check refuses, before it is allocated, storage of n bytes that would take the
// program past its bound: what it holds, and the temporaries of the statement
// that is running, are counted.
func (b *budget) check(f *frame, n int64) {
	if b == nil || b.maxMem <= 0 {
		return
	}
	if n > b.maxMem-b.mem-b.tmp {
		fail(f, "memory limit exceeded: %d more bytes would pass the limit of %d bytes (%d held, %d in temporaries)", n, b.maxMem, b.mem, b.tmp)
	}
}

// peek reserves n bytes for a temporary (the value of an array expression, a
// section, a constructor, a character result): it is refused when it would
// take the program past its bound together with what it holds and the
// temporaries already alive in the statement. The reservation is released when
// the next statement that makes temporaries starts (compiler.stmts resets it to
// the caller's), because a temporary is garbage when its statement ends; two
// temporaries of one statement are alive together, so they are summed.
func (b *budget) peek(f *frame, n int64) {
	if b == nil || b.maxMem <= 0 {
		return
	}
	b.check(f, n)
	b.tmp += n
}

// underflow is the failure of a credit that gives back more than was charged:
// a defect of the evaluator, reported and never hidden by clamping.
func underflow(f *frame, what string, have, n int64) {
	fail(f, "internal error in the evaluator: the memory counter would go below zero (%s holds %d, credit of %d); please report it", what, have, n)
}

// charge counts n bytes of storage that live in the frame own, or only checks
// them when own is nil (a temporary).
func (b *budget) charge(f *frame, own *frame, n int64) {
	if b == nil {
		return
	}
	b.check(f, n)
	if own == nil {
		return
	}
	b.mem += n
	own.mem += n
}

// credit gives back n bytes charged to own.
func (b *budget) credit(f *frame, own *frame, n int64) {
	if b == nil || n <= 0 {
		return
	}
	if own.mem < n {
		underflow(f, "the frame", own.mem, n)
	}
	if b.mem < n {
		underflow(f, "the session", b.mem, n)
	}
	b.mem -= n
	own.mem -= n
}

// chargeDelta follows a variable that changes size by d bytes.
func (b *budget) chargeDelta(f *frame, own *frame, d int64) {
	if d > 0 {
		b.charge(f, own, d)
	} else {
		b.credit(f, own, -d)
	}
}

// exit credits what the frame of a procedure that returns still holds, and
// marks the frame dead, so that storage charged to it and credited with it is
// not credited a second time by release (a guard: the compiler refuses an
// allocatable dummy argument today, the one case where an array charged to a
// callee's frame would be reached by its caller).
func (b *budget) exit(f *frame) {
	if b == nil {
		return
	}
	if b.aliased && b.heap != nil {
		// an array of this frame that a pointer was associated with outlives
		// the frame in fact (the pointer still holds it, and Go keeps it): its
		// charge moves to the heap rather than being credited, so the memory
		// stays counted. A pointer to a local that is not SAVEd is undefined
		// in Fortran after the return; counting it is the safe side.
		for _, g := range f.gas {
			if g != nil && g.aliased && g.own == f && g.chg > 0 {
				f.mem -= g.chg
				b.heap.mem += g.chg
				g.own = b.heap
			}
		}
	}
	if b.mem < f.mem {
		underflow(f, "the session", b.mem, f.mem)
	}
	b.mem -= f.mem
	f.mem = 0
	f.dead = true
	b.tmp = f.tmpBase // the callee's temporaries are garbage; the caller's stay
}

// held is what the frames that outlive a cell hold: the counter's start for the
// next one.
func held(mods []*frame) int64 {
	var n int64
	for _, m := range mods {
		n += m.mem
	}
	return n
}

// mark is the temporaries reserved now, and reset(v) goes back to a mark: the
// loops that evaluate an expression again and again in one statement.
func (b *budget) mark() int64 {
	if b == nil {
		return 0
	}
	return b.tmp
}

func (b *budget) reset(v int64) {
	if b != nil {
		b.tmp = v
	}
}

// valBytes is what a value of an output list costs, as it waits for the
// formatting.
const valBytes = 48

// valRoom is the number of values an output statement may still build: the
// output the cell may still write, at no more than two bytes a value (a
// separator and a character), because the values past it could only print after
// the cap, where they are dropped; and no more than the budget has room for.
// The text is then never built whole for an output that will be cut.
func (b *budget) valRoom() int {
	if b == nil || b.outCap <= 0 {
		return math.MaxInt
	}
	return int(min(b.outLeft/2+1, math.MaxInt32))
}

// valsFit reserves the storage of n values of an output list.
func (b *budget) valsFit(f *frame, n int) {
	b.peek(f, int64(n)*valBytes)
}

// countWriter counts what a cell writes against the output cap.
type countWriter struct {
	w io.Writer
	b *budget
}

func (c *countWriter) Write(p []byte) (int, error) {
	c.b.outLeft = max(0, c.b.outLeft-int64(len(p)))
	return c.w.Write(p)
}

// garrBytes is the storage of an array of n elements.
func garrBytes(n int) int64 { return 8*int64(n) + arrayOverhead }

// retain charges the array g, just made, to own, and remembers it so that
// deallocate credits it.
func (b *budget) retain(f *frame, own *frame, g *garr) *garr {
	if b == nil || g == nil || own == nil {
		return g
	}
	n := garrBytes(g.size())
	// the array was reserved as a temporary when it was made; it is held now
	b.tmp = max(0, b.tmp-n)
	b.charge(f, own, n)
	g.chg, g.own = n, own
	return g
}

// release credits what g was charged and forgets it. An array charged to a
// frame that has returned was credited when the frame did.
func (b *budget) release(f *frame, g *garr) {
	if b == nil || g == nil || g.chg == 0 {
		return
	}
	if !g.own.dead {
		b.credit(f, g.own, g.chg)
	}
	g.chg, g.own = 0, nil
}

// objBytes is the storage of a derived-type object of type t, its own
// components included but its array components not (they are charged when they
// are made).
func (t *dtype) objBytes() int64 {
	n := int64(objectOverhead + slotBytes*len(t.comps))
	for _, dc := range t.comps {
		if dc.k == kStr {
			n += min(dc.clen, MaxStringLen)
		}
	}
	return n
}
