package eval

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

// peek refuses, before it is allocated, a temporary of n bytes that would take
// the program past its bound.
func (b *budget) peek(f *frame, n int64) {
	if b == nil || b.maxMem <= 0 {
		return
	}
	if n > b.maxMem-b.mem {
		fail(f, "memory limit exceeded: %d more bytes would pass the limit of %d bytes (%d held)", n, b.maxMem, b.mem)
	}
}

// charge counts n bytes of storage that live in the frame own.
func (b *budget) charge(f *frame, own *frame, n int64) {
	if b == nil {
		return
	}
	b.peek(f, n)
	if own == nil { // a temporary: checked, not held
		return
	}
	b.mem += n
	own.mem += n
}

// credit gives back n bytes charged to own.
func (b *budget) credit(own *frame, n int64) {
	if b == nil || n <= 0 {
		return
	}
	b.mem = max(0, b.mem-n)
	own.mem = max(0, own.mem-n)
}

// chargeDelta follows a variable that changes size by d bytes.
func (b *budget) chargeDelta(f *frame, own *frame, d int64) {
	if d > 0 {
		b.charge(f, own, d)
	} else {
		b.credit(own, -d)
	}
}

// exit credits what the frame of a procedure that returns still holds.
func (b *budget) exit(f *frame) {
	if b == nil {
		return
	}
	b.mem = max(0, b.mem-f.mem)
	f.mem = 0
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

// garrBytes is the storage of an array of n elements.
func garrBytes(n int) int64 { return 8*int64(n) + arrayOverhead }

// retain charges the array g, just made, to own, and remembers it so that
// deallocate credits it.
func (b *budget) retain(f *frame, own *frame, g *garr) *garr {
	if b == nil || g == nil || own == nil {
		return g
	}
	n := garrBytes(g.size())
	b.charge(f, own, n)
	g.chg, g.own = n, own
	return g
}

// release credits what g was charged and forgets it.
func (b *budget) release(g *garr) {
	if b == nil || g == nil || g.chg == 0 {
		return
	}
	b.credit(g.own, g.chg)
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
