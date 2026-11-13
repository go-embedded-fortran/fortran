package syntax

import (
	"fmt"
	"io/fs"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// The bomb tests feed hostile inputs and assert a REFUSAL (an error naming the
// bound), within a time and an allocation budget, under a soft memory limit
// (the equivalent of GOMEMLIMIT for the test process; CI also runs them with
// GOMEMLIMIT set).

const bombMemLimit = 512 << 20

func bomb(t *testing.T, name string, src string, opts *Options, want string) {
	t.Helper()
	old := debug.SetMemoryLimit(bombMemLimit)
	defer debug.SetMemoryLimit(old)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	f, err := Parse(name, []byte(src), opts)
	d := time.Since(start)
	runtime.ReadMemStats(&after)
	if f == nil {
		t.Fatal("nil File")
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		msg := "<nil>"
		if err != nil {
			msg = err.Error()
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		t.Fatalf("%s: want a refusal containing %q, got %s", name, want, msg)
	}
	if strings.Contains(err.Error(), "internal parser error") {
		t.Fatalf("%s: %v", name, err)
	}
	if d > 20*time.Second {
		t.Errorf("%s: took %v", name, d)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<30 {
		t.Errorf("%s: allocated %d MiB", name, alloc>>20)
	}
}

func TestBombSourceBytes(t *testing.T) {
	src := "x = 1\n" + strings.Repeat("y = 2\n", 1<<18)
	bomb(t, "big.f90", src, &Options{Limits: Limits{MaxSourceBytes: 1 << 20}}, "over the limit")
}

func TestBombNestingParens(t *testing.T) {
	src := "x = " + strings.Repeat("(", 1<<16) + "1" + strings.Repeat(")", 1<<16) + "\nend\n"
	bomb(t, "parens.f90", src, nil, "nesting deeper than 200: refused")
}

func TestBombNestingUnary(t *testing.T) {
	src := "x = " + strings.Repeat(".not. ", 1<<16) + "a\nend\n"
	bomb(t, "not.f90", src, nil, "nesting deeper")
	src = "x = " + strings.Repeat("a**", 1<<16) + "a\nend\n"
	bomb(t, "pow.f90", src, nil, "nesting deeper")
}

func TestBombNestingConstructs(t *testing.T) {
	src := strings.Repeat("do\n", 1<<16) + "end\n"
	bomb(t, "do.f90", src, nil, "nesting deeper")
	src = strings.Repeat("if (a) then\n", 1<<16) + "end\n"
	bomb(t, "if.f90", src, nil, "nesting deeper")
	src = strings.Repeat("if (a) ", 1<<14) + "x = 1\nend\n"
	bomb(t, "logif.f90", src, nil, "nesting deeper")
	src = strings.Repeat("interface\n", 1<<14) + "end\n"
	bomb(t, "iface.f90", src, nil, "nesting deeper")
}

func TestBombNestingArrays(t *testing.T) {
	src := "x = " + strings.Repeat("[", 1<<16) + "1" + strings.Repeat("]", 1<<16) + "\nend\n"
	bomb(t, "arr.f90", src, nil, "nesting deeper")
	src = "x = a" + strings.Repeat("(1,", 1<<16) + "1\nend\n"
	bomb(t, "index.f90", src, nil, "nesting deeper")
}

func TestBombContinuations(t *testing.T) {
	// the standard's limit (F2018 6.3.2.6): 255 continuation lines pass, 256 are refused
	ok := "x = 1" + strings.Repeat(" + &\n 1", 255) + "\nend\n"
	if _, err := Parse("c255.f90", []byte(ok), nil); err != nil {
		t.Fatalf("255 continuation lines must be accepted: %v", err)
	}
	over := "x = 1" + strings.Repeat(" + &\n 1", 256) + "\nend\n"
	bomb(t, "c256.f90", over, nil, "more than 255 continuation lines")
	huge := "x = 1" + strings.Repeat(" + &\n 1", 1<<20) + "\nend\n"
	bomb(t, "chuge.f90", huge, nil, "more than 255 continuation lines")
	fixed := "      X = 1\n" + strings.Repeat("     &    + 1\n", 300) + "      END\n"
	bomb(t, "cfixed.f", fixed, nil, "more than 255 continuation lines")
}

func TestBombIncludes(t *testing.T) {
	self := fstest.MapFS{"a.inc": {Data: []byte("include 'a.inc'\n")}}
	bomb(t, "self.f90", "include 'a.inc'\nend\n", &Options{FS: self}, "recursive include")
	// a chain deeper than the bound
	chain := fstest.MapFS{}
	for i := 0; i < 40; i++ {
		chain[fmt.Sprintf("f%d.inc", i)] = &fstest.MapFile{Data: []byte(fmt.Sprintf("include 'f%d.inc'\n", i+1))}
	}
	bomb(t, "chain.f90", "include 'f0.inc'\nend\n", &Options{FS: chain}, "nesting deeper than 8")
	// fan-out: every level includes the next 200 times, the budget must stop it
	fan := fstest.MapFS{}
	for i := 0; i < 4; i++ {
		fan[fmt.Sprintf("g%d.inc", i)] = &fstest.MapFile{Data: []byte(strings.Repeat(fmt.Sprintf("include 'g%d.inc'\n", i+1), 200))}
	}
	fan["g4.inc"] = &fstest.MapFile{Data: []byte(strings.Repeat("x = 1\n", 1000))}
	bomb(t, "fan.f90", "include 'g0.inc'\nend\n", &Options{FS: fan, Limits: Limits{MaxSourceBytes: 1 << 20, MaxErrors: 5}}, "refused")
}

func TestIncludeNeverHostFS(t *testing.T) {
	_, err := Parse("a.f90", []byte("include '/etc/passwd'\nend\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "no file system configured") {
		t.Fatalf("a nil FS must refuse includes, got %v", err)
	}
	mfs := fstest.MapFS{"ok.inc": {Data: []byte("integer :: i\n")}, "sub/x.inc": {Data: []byte("include 'y.inc'\n")}, "sub/y.inc": {Data: []byte("integer :: j\n")}}
	for _, name := range []string{"/etc/passwd", "../ok.inc", "sub/../../ok.inc"} {
		_, err = Parse("a.f90", []byte("include '"+name+"'\nend\n"), &Options{FS: mfs})
		if err == nil {
			t.Errorf("include %q must be refused", name)
		}
	}
	f, err := Parse("a.f90", []byte("include 'ok.inc'\ninclude './sub/x.inc'\nend\n"), &Options{FS: fs.FS(mfs)})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.Units[0].Body); n != 2 {
		t.Fatalf("included declarations: %d statements", n)
	}
	if got := f.Position(f.Units[0].Body[0].P()).String(); got != "ok.inc:1:1" {
		t.Errorf("position inside an include: %s", got)
	}
}

func TestBombManyErrors(t *testing.T) {
	src := strings.Repeat("= = =\n", 1<<20)
	bomb(t, "garbage.f90", src, &Options{Limits: Limits{MaxSourceBytes: 16 << 20}}, "too many errors")
}

func TestBombLongStatement(t *testing.T) {
	src := "x = 1" + strings.Repeat("+1", 1<<20) + "\nend\n"
	bomb(t, "long.f90", src, nil, "tokens")
}

func TestBombBinaryGarbage(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1<<16; i++ {
		b.WriteByte(byte(i * 7919))
	}
	f, err := Parse("g.f90", []byte(b.String()), nil)
	if f == nil || err == nil {
		t.Fatal("binary garbage must be refused with errors")
	}
}
