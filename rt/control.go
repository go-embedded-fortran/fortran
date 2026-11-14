// Format control (F2018 13.4): reversion, repeat counts and nested groups.
//
// Ported from LLVM flang-rt, flang-rt/include/flang-rt/runtime/
// format-implementation.h (FormatControl::CueUpNextDataEdit) at tag
// llvmorg-23.1.4.
// Part of the LLVM Project, under the Apache License v2.0 with LLVM
// Exceptions. See https://llvm.org/LICENSE.txt and the NOTICE file.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
// Changes: the format is compiled once (format.go) instead of being scanned
// as it is used, so the walk is over tokens.

package rt

// maxCtrlSteps bounds the format items one statement may process, so that a
// format such as 1000000000(/) is refused rather than run.
const maxCtrlSteps = 1 << 24

// ctlHandler receives the control edit descriptors and the record advances
// that format control performs between data edit descriptors.
type ctlHandler interface {
	ctrl(t *tok) *Error
	endRecord() *Error // the implied slash before the final ')' on reversion
}

type frame struct {
	open int
	left int // iterations remaining after the current one; -1 unlimited
}

// fctl is format control (F2018 13.4): it walks a compiled format and hands
// out data edit descriptors, performing the control edit descriptors it
// passes. Reversion follows 13.4 paragraph 9 and flang-rt's CueUpNextDataEdit.
type fctl struct {
	f      *Format
	pc     int
	stack  []frame
	rep    int
	cur    *tok
	revert int
	hitEnd bool
	steps  int
	buf    [8]frame
}

func (c *fctl) init(f *Format) {
	c.f = f
	c.pc = 1
	c.stack = append(c.buf[:0], frame{open: 0, left: -1})
	c.rep, c.cur, c.revert, c.hitEnd, c.steps = 0, nil, 0, false, 0
}

// next returns the next data edit descriptor, or nil when format control
// stops: at a colon or the end of the format with no item left (haveItem is
// false), which is also when the trailing control edits have been performed.
func (c *fctl) next(haveItem bool, h ctlHandler) (*tok, *Error) {
	if c.rep > 0 && haveItem {
		c.rep--
		return c.cur, nil
	}
	if c.rep > 0 {
		return nil, nil
	}
	toks := c.f.toks
	for {
		c.steps++
		if c.steps > maxCtrlSteps {
			return nil, errf(ErrFormat, "Format processing exceeds %d items", maxCtrlSteps)
		}
		t := &toks[c.pc]
		switch t.kind {
		case tOpen:
			if len(c.stack) == 1 && !c.hitEnd {
				c.revert = c.pc
			}
			left := t.rep - 1
			if t.rep < 0 {
				left = -1
			}
			c.stack = append(c.stack, frame{open: c.pc, left: left})
			c.pc++
		case tClose:
			if len(c.stack) == 1 {
				c.hitEnd = true
				if !haveItem {
					return nil, nil
				}
				if !c.f.hasData {
					return nil, errf(ErrFormat, "Format has no data edit descriptor for the remaining items")
				}
				if e := h.endRecord(); e != nil {
					return nil, e
				}
				c.stack = c.stack[:1]
				if c.revert != 0 {
					c.pc = c.revert
				} else {
					c.pc = 1
				}
				continue
			}
			fr := &c.stack[len(c.stack)-1]
			switch {
			case fr.left < 0:
				if !haveItem && !c.f.hasData {
					return nil, nil
				}
				c.pc = fr.open + 1
			case fr.left > 0:
				fr.left--
				c.pc = fr.open + 1
			default:
				c.stack = c.stack[:len(c.stack)-1]
				c.pc++
			}
		case tData:
			if !haveItem {
				return nil, nil
			}
			c.rep, c.cur = t.rep-1, t
			c.pc++
			return t, nil
		default:
			if t.kind == tCtrl && t.c == ':' {
				if !haveItem {
					return nil, nil
				}
				c.pc++
				continue
			}
			if e := h.ctrl(t); e != nil {
				return nil, e
			}
			c.pc++
		}
	}
}
