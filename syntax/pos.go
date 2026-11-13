package syntax

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Pos is a position in a FileSet: a global byte offset, 0 meaning "none".
type Pos int32

// Position is a Pos resolved to a file, line and column (both 1-based, the
// column counted in bytes).
type Position struct {
	File string
	Line int
	Col  int
}

func (p Position) String() string {
	if p.File == "" && p.Line == 0 {
		return "-"
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// FileSet holds the source files of one parse (the main file and its
// includes) and resolves positions.
type FileSet struct {
	files []*srcFile
	size  int
}

type srcFile struct {
	name  string
	base  int
	src   []byte
	lines []int32 // offsets of line starts, built lazily
}

func (fs *FileSet) add(name string, src []byte) *srcFile {
	f := &srcFile{name: name, base: fs.size + 1, src: src}
	fs.files = append(fs.files, f)
	fs.size += len(src) + 1
	return f
}

func (f *srcFile) pos(off int) Pos { return Pos(f.base + off) }

func (f *srcFile) lineStarts() []int32 {
	if f.lines == nil {
		f.lines = append(f.lines, 0)
		for i, c := range f.src {
			if c == '\n' {
				f.lines = append(f.lines, int32(i+1))
			}
		}
	}
	return f.lines
}

// Position resolves p; the zero Position is returned for NoPos.
func (fs *FileSet) Position(p Pos) Position {
	if p <= 0 || fs == nil {
		return Position{}
	}
	i := sort.Search(len(fs.files), func(i int) bool { return fs.files[i].base > int(p) }) - 1
	if i < 0 {
		return Position{}
	}
	f := fs.files[i]
	off := int(p) - f.base
	ls := f.lineStarts()
	l := sort.Search(len(ls), func(k int) bool { return int(ls[k]) > off }) - 1
	return Position{File: f.name, Line: l + 1, Col: off - int(ls[l]) + 1}
}

// Error is one diagnostic, "file:line:col: message".
type Error struct {
	Pos Position
	Msg string
}

func (e Error) Error() string { return e.Pos.String() + ": " + e.Msg }

// ErrorList is the diagnostics of one parse; it implements error.
type ErrorList []Error

func (l ErrorList) Error() string {
	switch len(l) {
	case 0:
		return "no errors"
	case 1:
		return l[0].Error()
	}
	var b strings.Builder
	for i, e := range l {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Error())
	}
	return b.String()
}

// Form is a source form (F2018 6.3).
type Form uint8

const (
	// Auto chooses the form from the file name (DetectForm).
	Auto Form = iota
	// Free is free source form, 6.3.2.
	Free
	// Fixed is fixed source form, 6.3.3.
	Fixed
)

func (f Form) String() string {
	switch f {
	case Free:
		return "free"
	case Fixed:
		return "fixed"
	}
	return "auto"
}

// DetectForm returns the source form implied by a file name, as gfortran
// does: .f .for .f77 .ftn (any case) are fixed, everything else is free.
func DetectForm(name string) Form {
	switch strings.ToLower(path.Ext(name)) {
	case ".f", ".for", ".f77", ".ftn", ".fpp":
		return Fixed
	}
	return Free
}

// Limits bound what an untrusted source may cost. Zero fields take the
// defaults of DefaultLimits.
type Limits struct {
	MaxSourceBytes   int // main file plus all includes (default 16 MiB)
	MaxDepth         int // nesting of constructs and of expressions (default 200)
	MaxIncludeDepth  int // nested INCLUDE lines (default 8)
	MaxContinuations int // continuation lines of one statement (default 255, F2018 6.3.2.6)
	MaxErrors        int // reported errors before the parse gives up (default 50)
	MaxStatementToks int // tokens of one statement (default 1<<20)
}

// DefaultLimits returns the defaults.
func DefaultLimits() Limits {
	return Limits{MaxSourceBytes: 16 << 20, MaxDepth: 200, MaxIncludeDepth: 8, MaxContinuations: 255, MaxErrors: 50, MaxStatementToks: 1 << 20}
}

func (l Limits) fill() Limits {
	d := DefaultLimits()
	if l.MaxSourceBytes <= 0 {
		l.MaxSourceBytes = d.MaxSourceBytes
	}
	if l.MaxSourceBytes > 1<<30 {
		l.MaxSourceBytes = 1 << 30
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxIncludeDepth <= 0 {
		l.MaxIncludeDepth = d.MaxIncludeDepth
	}
	if l.MaxContinuations <= 0 {
		l.MaxContinuations = d.MaxContinuations
	}
	if l.MaxErrors <= 0 {
		l.MaxErrors = d.MaxErrors
	}
	if l.MaxStatementToks <= 0 {
		l.MaxStatementToks = d.MaxStatementToks
	}
	return l
}

// Options configure a parse; the zero value is valid.
type Options struct {
	Form   Form  // Auto: DetectForm(name)
	FS     fs.FS // resolves INCLUDE; nil refuses every INCLUDE
	Limits Limits
}
