package syntax

import (
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
)

// seg maps a run of logical-line text to a run of source positions.
type seg struct {
	at  int32
	pos Pos
}

// logLine is one logical line: the statement text of a source line and its
// continuation lines joined, comments removed, with a position map.
type logLine struct {
	text []byte
	segs []seg
	last Pos
}

func (l *logLine) reset() { l.text, l.segs, l.last = l.text[:0], l.segs[:0], 0 }

func (l *logLine) add(c byte, p Pos) {
	if len(l.segs) == 0 || p != l.last+1 {
		l.segs = append(l.segs, seg{int32(len(l.text)), p})
	}
	l.last = p
	l.text = append(l.text, c)
}

// posAt resolves an index of text to a source position.
func (l *logLine) posAt(i int) Pos {
	if len(l.segs) == 0 {
		return 0
	}
	k := sort.Search(len(l.segs), func(k int) bool { return int(l.segs[k].at) > i }) - 1
	if k < 0 {
		k = 0
	}
	return l.segs[k].pos + Pos(i-int(l.segs[k].at))
}

type fileState struct {
	f    *srcFile
	off  int
	dir  string
	path string // resolved include path, "" for the main file
}

// scanner turns source files into logical lines, resolving INCLUDE lines.
type scanner struct {
	fset  *FileSet
	opts  *Options
	form  Form
	lim   Limits
	errf  func(Pos, string)
	stack []*fileState
	total int
	line  logLine
	tmp   logLine
	pcs   []piece
}

type piece struct{ a, b int }

func (s *scanner) push(f *srcFile, dir, p string) {
	s.stack = append(s.stack, &fileState{f: f, dir: dir, path: p})
}

// next returns the next logical line; the result is valid until the next call.
func (s *scanner) next() (*logLine, bool) {
	for len(s.stack) > 0 {
		st := s.stack[len(s.stack)-1]
		if st.off >= len(st.f.src) {
			s.stack = s.stack[:len(s.stack)-1]
			continue
		}
		var ok bool
		if s.form == Fixed {
			ok = s.readFixed(st)
		} else {
			ok = s.readFree(st)
		}
		if !ok {
			continue
		}
		if name, ok := includeName(s.line.text); ok {
			s.include(st, name, s.line.posAt(0))
			continue
		}
		return &s.line, true
	}
	return nil, false
}

// lineAt returns the bounds of the physical line at off (without the line
// terminator) and the offset of the next one.
func lineAt(src []byte, off int) (ls, le, next int) {
	ls = off
	i := off
	for i < len(src) && src[i] != '\n' {
		i++
	}
	le = i
	next = i
	if i < len(src) {
		next = i + 1
	}
	if le > ls && src[le-1] == '\r' {
		le--
	}
	return
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\f' }

func skipBlank(src []byte, i, e int) int {
	for i < e && isBlank(src[i]) {
		i++
	}
	return i
}

// restBlankOrComment reports whether src[i:e] is blanks, or blanks then "!".
func restBlankOrComment(src []byte, i, e int, comment bool) bool {
	i = skipBlank(src, i, e)
	return i == e || (comment && src[i] == '!')
}

// readFree reads one free-form statement line (F2018 6.3.2): "!" comments,
// "&" continuation (non-character and character context, 6.3.2.4), at most
// Limits.MaxContinuations continuation lines (6.3.2.6).
func (s *scanner) readFree(st *fileState) bool {
	src := st.f.src
	b := &s.line
	b.reset()
	var quote byte
	continuing := false
	conts := 0
	overflow := false
	var firstPos Pos
	for st.off < len(src) {
		ls, le, next := lineAt(src, st.off)
		st.off = next
		i := skipBlank(src, ls, le)
		if continuing {
			if i == le || src[i] == '!' {
				continue // comment and blank lines may occur within a continued statement
			}
			if src[i] == '&' {
				i++
			} else if quote != 0 {
				// F2018 6.3.2.4 requires the '&'; gfortran only warns, and so
				// do we not: the literal continues at the first column
				i = ls
			}
			continuing = false
			conts++
			if conts > s.lim.MaxContinuations && !overflow {
				overflow = true
				s.errf(st.f.pos(i), fmt.Sprintf("statement has more than %d continuation lines (F2018 6.3.2.6): refused", s.lim.MaxContinuations))
			}
		} else {
			if i == le || src[i] == '!' || (src[i] == '#' && i == ls) {
				continue // a cpp directive of a .F90 file is skipped, not interpreted
			}
			firstPos = st.f.pos(i)
		}
	scan:
		for ; i < le; i++ {
			c := src[i]
			if quote != 0 {
				if c == quote {
					if !overflow {
						b.add(c, st.f.pos(i))
					}
					if i+1 < le && src[i+1] == quote {
						i++
						if !overflow {
							b.add(c, st.f.pos(i))
						}
					} else {
						quote = 0
					}
					continue
				}
				if c == '&' && restBlankOrComment(src, i+1, le, false) {
					continuing = true
					break scan
				}
				if !overflow {
					b.add(c, st.f.pos(i))
				}
				continue
			}
			switch c {
			case '!':
				break scan
			case '&':
				if restBlankOrComment(src, i+1, le, true) {
					continuing = true
					break scan
				}
			case '\'', '"':
				quote = c
			}
			if !overflow {
				b.add(c, st.f.pos(i))
			}
		}
		if !continuing {
			if overflow {
				b.reset()
				return false
			}
			return len(b.text) > 0
		}
	}
	if continuing {
		s.errf(firstPos, "continued statement ends at the end of the file")
	}
	if overflow {
		b.reset()
		return false
	}
	return len(b.text) > 0
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

func hasPrefixFold(b []byte, p string) bool {
	if len(b) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if lower(b[i]) != p[i] {
			return false
		}
	}
	return true
}

// readFixed reads one fixed-form statement (F2018 6.3.3): label in columns
// 1-5, continuation in column 6, text in columns 7-72, "C", "*" or "!" comments.
func (s *scanner) readFixed(st *fileState) bool {
	src := st.f.src
	s.pcs = s.pcs[:0]
	var quote byte
	var label []int // source offsets of the label digits
	started := false
	conts := 0
	overflow := false
	var firstPos Pos
	for st.off < len(src) {
		ls, le, next := lineAt(src, st.off)
		line := src[ls:le]
		j := skipBlank(line, 0, len(line))
		if j == len(line) {
			st.off = next
			continue // blank line: a comment line (6.3.3.2)
		}
		switch line[0] {
		case 'c', 'C', '*', '!', 'd', 'D', '#':
			st.off = next
			continue
		}
		if line[j] == '!' && j != 5 {
			st.off = next
			continue
		}
		tab := -1
		for k := 0; k < len(line) && k < 6; k++ {
			if line[k] == '\t' {
				tab = k
				break
			}
		}
		var contFlag bool
		var lab []byte
		textStart, textEnd := 6, len(line)
		if tab >= 0 {
			lab = line[:tab]
			textStart = tab + 1
			if textStart < len(line) && line[textStart] >= '1' && line[textStart] <= '9' {
				contFlag = true
				textStart++
			}
		} else {
			if len(line) > 5 {
				lab = line[:5]
				contFlag = line[5] != ' ' && line[5] != '0'
			} else {
				lab = line
			}
			if textEnd > 72 {
				textEnd = 72
			}
		}
		if textStart > textEnd {
			textStart = textEnd
		}
		if !contFlag && started {
			break // initial line of the next statement; not consumed
		}
		st.off = next
		if contFlag && started {
			conts++
			if conts > s.lim.MaxContinuations && !overflow {
				overflow = true
				s.errf(st.f.pos(ls), fmt.Sprintf("statement has more than %d continuation lines (F2018 6.3.3.5): refused", s.lim.MaxContinuations))
			}
		} else {
			firstPos = st.f.pos(ls + textStart)
			for k, c := range lab {
				if c >= '0' && c <= '9' {
					label = append(label, ls+k)
				} else if !isBlank(c) {
					s.errf(st.f.pos(ls+k), "invalid character in the label field (F2018 6.3.3.5)")
					break
				}
			}
		}
		started = true
		// find the comment start outside a character context
		cut := textEnd
		for k := textStart; k < textEnd; k++ {
			c := line[k]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '!' {
				cut = k
				break
			}
			if c == '\'' || c == '"' {
				quote = c
			}
		}
		if cut < textEnd {
			quote = 0
		}
		if !overflow {
			s.pcs = append(s.pcs, piece{ls + textStart, ls + cut})
		}
	}
	if !started {
		return false
	}
	_ = firstPos
	if overflow {
		return false
	}
	return s.buildFixed(st.f, label)
}

// buildFixed assembles the statement of s.pcs into s.line: blanks removed
// outside character context, a FORMAT statement kept raw, blanks re-inserted
// after the keywords.
func (s *scanner) buildFixed(f *srcFile, label []int) bool {
	src := f.src
	t := &s.tmp
	t.reset()
	var quote byte
	for _, p := range s.pcs {
		for k := p.a; k < p.b; k++ {
			c := src[k]
			if quote != 0 {
				t.add(c, f.pos(k))
				if c == quote {
					quote = 0
				}
				continue
			}
			if isBlank(c) {
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
			}
			t.add(c, f.pos(k))
		}
	}
	b := &s.line
	b.reset()
	if len(t.text) == 0 && len(label) == 0 {
		return false
	}
	for _, k := range label {
		b.add(src[k], f.pos(k))
	}
	if len(label) > 0 && len(t.text) > 0 {
		b.add(' ', t.posAt(0))
	}
	if hasPrefixFold(t.text, "format(") {
		// FORMAT keeps its blanks (Hollerith, blanks in edit descriptors)
		first := true
		for _, p := range s.pcs {
			for k := p.a; k < p.b; k++ {
				if first && isBlank(src[k]) {
					continue
				}
				first = false
				b.add(src[k], f.pos(k))
			}
		}
		for len(b.text) > 0 && isBlank(b.text[len(b.text)-1]) {
			b.text = b.text[:len(b.text)-1]
		}
		return true
	}
	ins := classifyFixed(t.text)
	n := 0
	for i, c := range t.text {
		for n < len(ins) && ins[n] == i {
			b.add(' ', t.posAt(i))
			n++
		}
		b.add(c, t.posAt(i))
	}
	return true
}

// includeName recognises an INCLUDE line (F2018 6.4) and returns its literal.
func includeName(text []byte) (string, bool) {
	i := 0
	for i < len(text) && isBlank(text[i]) {
		i++
	}
	if !hasPrefixFold(text[i:], "include") {
		return "", false
	}
	i += 7
	for i < len(text) && isBlank(text[i]) {
		i++
	}
	if i >= len(text) || (text[i] != '\'' && text[i] != '"') {
		return "", false
	}
	q := text[i]
	var name []byte
	for i++; i < len(text); i++ {
		if text[i] == q {
			if i+1 < len(text) && text[i+1] == q {
				name = append(name, q)
				i++
				continue
			}
			rest := text[i+1:]
			for _, c := range rest {
				if !isBlank(c) {
					return "", false
				}
			}
			return string(name), true
		}
		name = append(name, text[i])
	}
	return "", false
}

func (s *scanner) include(st *fileState, name string, pos Pos) {
	if s.opts.FS == nil {
		s.errf(pos, "include '"+name+"': no file system configured, includes are refused")
		return
	}
	if len(s.stack) > s.lim.MaxIncludeDepth {
		s.errf(pos, fmt.Sprintf("include '%s': nesting deeper than %d: refused", name, s.lim.MaxIncludeDepth))
		return
	}
	clean := path.Clean(name)
	var cands []string
	if st.dir != "" && st.dir != "." {
		cands = append(cands, path.Join(st.dir, clean))
	}
	cands = append(cands, clean)
	for _, c := range cands {
		if !fs.ValidPath(c) {
			continue
		}
		for _, o := range s.stack {
			if o.path == c {
				s.errf(pos, "include '"+name+"': recursive include (F2018 6.4)")
				return
			}
		}
		fh, err := s.opts.FS.Open(c)
		if err != nil {
			continue
		}
		room := s.lim.MaxSourceBytes - s.total
		data, err := io.ReadAll(io.LimitReader(fh, int64(room)+1))
		fh.Close()
		if err != nil {
			s.errf(pos, "include '"+name+"': "+err.Error())
			return
		}
		if len(data) > room {
			s.errf(pos, fmt.Sprintf("include '%s': source exceeds %d bytes: refused", name, s.lim.MaxSourceBytes))
			return
		}
		s.total += len(data)
		f := s.fset.add(c, data)
		s.push(f, path.Dir(c), c)
		if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
			s.stack[len(s.stack)-1].off = 3 // UTF-8 byte order mark
		}
		return
	}
	s.errf(pos, "include '"+name+"': file not found or not a valid path")
}
