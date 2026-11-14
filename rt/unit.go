package rt

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"sync"
)

// IOSTAT values. The negative values are those of F2018 (16.10.2.17,
// 16.10.2.18); the positive ones are libgfortran's LIBERROR_* codes, so a
// program that prints its iostat prints what gfortran does.
const (
	ErrEOF          = -1
	ErrEOR          = -2
	ErrOS           = 5000
	ErrBadUnit      = 5005
	ErrFormat       = 5006
	ErrBadAction    = 5007
	ErrEndfile      = 5008
	ErrReadValue    = 5010
	ErrReadOverflow = 5011
	ErrInternal     = 5012
	ErrShortRecord  = 5016
)

// Error is the outcome of a data transfer statement that did not complete:
// the value of IOSTAT and the text of IOMSG. A nil *Error is success.
type Error struct {
	Stat int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("iostat=%d: %s", e.Stat, e.Msg) }

// IsEOF reports an end-of-file condition (the END= branch).
func (e *Error) IsEOF() bool { return e != nil && e.Stat == ErrEOF }

// IsEOR reports an end-of-record condition (the EOR= branch).
func (e *Error) IsEOR() bool { return e != nil && e.Stat == ErrEOR }

func errf(stat int, format string, a ...any) *Error {
	return &Error{Stat: stat, Msg: fmt.Sprintf(format, a...)}
}

// DefaultRecLen bounds one record of an external unit (libgfortran's default
// is 1 GiB; here a record lives in memory, so the bound is smaller). A unit's
// RecLen overrides it.
const DefaultRecLen = 1 << 24

// MaxFileSize bounds a file loaded from a file system.
const MaxFileSize = 64 << 20

// Unit is one connected unit: external (a writer, a reader, or an in-memory
// file) or internal (a character variable). A Unit is not safe for concurrent
// use.
type Unit struct {
	Num int

	// RecLen bounds a record (DefaultRecLen when zero).
	RecLen int

	// Default modes of a statement on the unit (the OPEN specifiers).
	Pad      bool // PAD= (true by default)
	BlankZ   bool // BLANK='ZERO'
	Delim    byte // 0, '\'' or '"'
	Round    Round
	DecComma bool

	out      io.Writer
	in       *bufio.Reader
	mem      [][]byte // in-memory file (records), when neither out nor in
	memIdx   int
	internal bool
	nrec     int // internal unit: record count
	ilen     int // internal unit: record length
	irec     int // internal unit: current record

	// output state
	rec     []byte
	pos     int
	pending bool // a record has been started (non-advancing output)

	// input state
	cur     []byte
	ipos    int
	haveRec bool
	atEOF   bool
	lastEOR bool

	canWrite, canRead bool

	// reusable statement state
	ost outStmt
	fc  fctl
}

// NewInternalWrite makes an internal unit over a character variable of nrec
// records of recLen characters each (a scalar variable has one record).
func NewInternalWrite(nrec, recLen int) *Unit {
	u := &Unit{internal: true, nrec: nrec, ilen: recLen, canWrite: true, Pad: true}
	u.mem = make([][]byte, nrec)
	for i := range u.mem {
		u.mem[i] = bytes.Repeat([]byte{' '}, recLen)
	}
	return u
}

// NewInternalRead makes an internal unit that reads from the given records
// (a scalar character variable is one record).
func NewInternalRead(recs ...string) *Unit {
	u := &Unit{internal: true, canRead: true, Pad: true, nrec: len(recs)}
	for _, r := range recs {
		u.mem = append(u.mem, []byte(r))
	}
	return u
}

// Records returns the records of an internal unit after output.
func (u *Unit) Records() []string {
	out := make([]string, len(u.mem))
	for i, r := range u.mem {
		out[i] = string(r)
	}
	return out
}

// NewWriter makes an external output unit that writes each record, followed
// by a newline, to w.
func NewWriter(num int, w io.Writer) *Unit {
	return &Unit{Num: num, out: w, canWrite: true, Pad: true}
}

// NewReader makes an external input unit that reads newline-terminated
// records from r.
func NewReader(num int, r io.Reader) *Unit {
	return &Unit{Num: num, in: bufio.NewReaderSize(r, 64<<10), canRead: true, Pad: true}
}

// NewMemory makes an in-memory sequential file, readable and writable;
// Rewind moves to its first record.
func NewMemory(num int) *Unit {
	return &Unit{Num: num, canRead: true, canWrite: true, Pad: true}
}

func (u *Unit) recLimit() int {
	if u.internal {
		return u.ilen
	}
	if u.RecLen > 0 {
		return u.RecLen
	}
	return DefaultRecLen
}

// ---- output records -------------------------------------------------------

// put writes b at the current position of the current record.
func (u *Unit) put(b []byte) *Error {
	if len(b) == 0 {
		return nil
	}
	end := u.pos + len(b)
	if end > u.recLimit() {
		if u.internal {
			return errf(ErrEOR, "End of record")
		}
		return errf(ErrOS, "Record length limit of %d exceeded", u.recLimit())
	}
	u.pending = true
	for len(u.rec) < u.pos {
		u.rec = append(u.rec, ' ')
	}
	if end <= len(u.rec) {
		copy(u.rec[u.pos:], b)
	} else {
		n := copy(u.rec[u.pos:], b)
		u.rec = append(u.rec, b[n:]...)
	}
	u.pos = end
	return nil
}

func (u *Unit) putByte(c byte) *Error {
	var a [1]byte
	a[0] = c
	return u.put(a[:])
}

// move sets the position (T, TL, TR, X).
func (u *Unit) move(pos int) *Error {
	if pos < 0 {
		pos = 0
	}
	if pos > u.recLimit() {
		if u.internal {
			return errf(ErrEOR, "End of record")
		}
		return errf(ErrOS, "Record length limit of %d exceeded", u.recLimit())
	}
	u.pending = true
	u.pos = pos
	return nil
}

// abortRecord handles an error in the middle of an output statement: like
// libgfortran, an external unit keeps what was already written, without the
// record terminator.
func (u *Unit) abortRecord() {
	if u.out != nil && len(u.rec) > 0 {
		u.out.Write(u.rec)
	}
	u.rec, u.pos, u.pending = u.rec[:0], 0, false
}

// endRecord finishes the current output record.
func (u *Unit) endRecord() *Error {
	defer func() { u.rec, u.pos, u.pending = u.rec[:0], 0, false }()
	switch {
	case u.out != nil:
		u.rec = append(u.rec, '\n')
		_, err := u.out.Write(u.rec)
		if err != nil {
			return errf(ErrOS, "%v", err)
		}
	case u.internal:
		if u.irec >= u.nrec {
			return errf(ErrEOR, "End of record")
		}
		r := u.mem[u.irec]
		n := copy(r, u.rec)
		for i := n; i < len(r); i++ {
			r[i] = ' '
		}
		u.irec++
	default:
		if !u.canWrite {
			return errf(ErrBadAction, "Cannot write to a file opened for reading")
		}
		u.mem = append(u.mem[:u.memIdx], append([]byte(nil), u.rec...))
		u.memIdx++
	}
	return nil
}

// ---- input records --------------------------------------------------------

// nextRecord loads the next input record.
func (u *Unit) nextRecord() *Error {
	u.haveRec, u.ipos, u.lastEOR = false, 0, false
	switch {
	case u.in != nil:
		var line []byte
		for {
			part, err := u.in.ReadSlice('\n')
			line = append(line, part...)
			if len(line) > u.recLimit() {
				return errf(ErrReadOverflow, "Record length limit of %d exceeded", u.recLimit())
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				if err == io.EOF && len(line) > 0 {
					break
				}
				u.atEOF = true
				if err == io.EOF {
					return errf(ErrEOF, "End of file")
				}
				return errf(ErrOS, "%v", err)
			}
			break
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		u.cur = line
	default:
		idx := u.irec
		if !u.internal {
			idx = u.memIdx
		}
		if idx >= len(u.mem) {
			u.atEOF = true
			return errf(ErrEOF, "End of file")
		}
		u.cur = u.mem[idx]
		if u.internal {
			u.irec++
		} else {
			u.memIdx++
		}
	}
	u.haveRec = true
	return nil
}

// Rewind positions an in-memory or file unit at its first record.
func (u *Unit) Rewind() *Error {
	if u.pending {
		if e := u.endRecord(); e != nil {
			return e
		}
	}
	u.memIdx, u.haveRec, u.atEOF, u.ipos = 0, false, false, 0
	if u.internal {
		u.irec = 0
	}
	if u.in != nil {
		return errf(ErrOS, "Cannot rewind a stream")
	}
	return nil
}

// Backspace positions an in-memory unit before the previous record.
func (u *Unit) Backspace() *Error {
	if u.in != nil || u.internal {
		return errf(ErrOS, "Cannot backspace this unit")
	}
	if u.memIdx > 0 {
		u.memIdx--
	}
	u.haveRec, u.atEOF = false, false
	return nil
}

// Endfile truncates an in-memory file after the current record.
func (u *Unit) Endfile() *Error {
	if u.in != nil || u.internal || !u.canWrite {
		return errf(ErrBadAction, "Cannot write an endfile record on this unit")
	}
	u.mem = u.mem[:u.memIdx]
	return nil
}

// Table is the unit table of a program: units 5 (standard input), 6
// (standard output) and 0 (standard error) are preconnected; others come from
// Open or from files of a read-only file system.
type Table struct {
	mu    sync.Mutex
	units map[int]*Unit
	fsys  fs.FS
}

// NewTable makes a unit table with units 5, 6 and 0 connected to stdin,
// stdout and stderr (nil connects nothing). fsys, when not nil, is the
// read-only file system that OpenFile reads.
func NewTable(stdout, stderr io.Writer, stdin io.Reader, fsys fs.FS) *Table {
	t := &Table{units: map[int]*Unit{}, fsys: fsys}
	if stdout != nil {
		t.units[6] = NewWriter(6, stdout)
	}
	if stderr != nil {
		t.units[0] = NewWriter(0, stderr)
	}
	if stdin != nil {
		t.units[5] = NewReader(5, stdin)
	}
	return t
}

// Get returns a connected unit or nil.
func (t *Table) Get(n int) *Unit {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.units[n]
}

// Connect connects u to unit number n, replacing any earlier connection.
func (t *Table) Connect(n int, u *Unit) {
	t.mu.Lock()
	u.Num = n
	t.units[n] = u
	t.mu.Unlock()
}

// OpenFile connects unit n to a file of the read-only file system (the whole
// file is read, at most MaxFileSize bytes, into records).
func (t *Table) OpenFile(n int, name string) *Error {
	if t.fsys == nil {
		return errf(ErrOS, "No file system is available")
	}
	f, err := t.fsys.Open(name)
	if err != nil {
		return errf(ErrOS, "Cannot open file '%s': %v", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return errf(ErrOS, "%v", err)
	}
	if len(data) > MaxFileSize {
		return errf(ErrOS, "File '%s' is larger than %d bytes", name, MaxFileSize)
	}
	u := NewMemory(n)
	u.canWrite = false
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		var line []byte
		if i < 0 {
			line, data = data, nil
		} else {
			line, data = data[:i], data[i+1:]
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		u.mem = append(u.mem, line)
	}
	t.Connect(n, u)
	return nil
}

// OpenScratch connects unit n to a new in-memory file (what OPEN of a new
// file does when there is no writable file system).
func (t *Table) OpenScratch(n int) *Unit {
	u := NewMemory(n)
	t.Connect(n, u)
	return u
}

// Close disconnects unit n, finishing a pending record.
func (t *Table) Close(n int) *Error {
	t.mu.Lock()
	u := t.units[n]
	delete(t.units, n)
	t.mu.Unlock()
	if u != nil && u.pending {
		return u.endRecord()
	}
	return nil
}

// Flush finishes pending (non-advancing) output records of every unit.
func (t *Table) Flush() {
	t.mu.Lock()
	var nums []int
	for n := range t.units {
		nums = append(nums, n)
	}
	t.mu.Unlock()
	sort.Ints(nums)
	for _, n := range nums {
		if u := t.Get(n); u != nil && u.pending {
			u.endRecord()
		}
	}
}
