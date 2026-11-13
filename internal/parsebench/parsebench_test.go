package parsebench

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
)

func TestRateTestdata(t *testing.T) {
	for _, dir := range []string{"../../testdata/programs", "../../testdata/gate"} {
		es, err := corpus.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(es) == 0 {
			t.Fatalf("%s: no files", dir)
		}
		recs := Rate(context.Background(), nil, dir, es, 1)
		s := Summarize(recs)
		if s.Parsed != len(es) {
			t.Errorf("%s: %d of %d parse: %+v", dir, s.Parsed, len(es), recs)
		}
		for _, r := range recs {
			if r.Verdict != NoOracle {
				t.Errorf("%s: verdict %q without an oracle", r.Path, r.Verdict)
			}
		}
	}
}

func TestReportAndCache(t *testing.T) {
	yes, no := true, false
	recs := []Rec{
		{Path: "a.f90", Bytes: 3, OursOK: true, Gf: &yes, Verdict: Both},
		{Path: "b.f", Bytes: 5, OursOK: false, First: "b.f:1:7: boom", Gf: &yes, Verdict: GfOnly},
		{Path: "c.f90", Bytes: 7, OursOK: false, First: "c.f90:2:1: boom", Gf: &no, Verdict: Neither},
	}
	var buf bytes.Buffer
	Report(&buf, recs)
	c := ReadCache(&buf)
	if len(c) != 3 || !c["a.f90"] || !c["b.f"] || c["c.f90"] {
		t.Errorf("cache %v", c)
	}
	s := Summarize(recs)
	if s.Files != 3 || s.Parsed != 1 || s.Both != 1 || s.GfOnly != 1 || s.Neither != 1 || s.GfAccepted != 2 {
		t.Errorf("summary %+v", s)
	}
	top := TopErrors(recs, 5)
	if len(top) != 1 || top[0].Count != 2 || !strings.Contains(top[0].Msg, "boom") {
		t.Errorf("top errors %+v", top)
	}
}

func TestBenchRefusesWithoutHome(t *testing.T) {
	t.Setenv("FORACLE_HOME", "")
	// either the load refusal or the missing durable directory: never a measurement
	_, err := Bench(context.Background(), nil, ".", nil, nil, 1, 1)
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	Table(&buf, []Row{{Path: "x.f90", Bytes: 1000, OursSec: 0.001, GfortranSec: 0.2, StartupSec: 0.1, OursMBps: 1, Ratio: 200, RatioNetStart: 100}})
	if !strings.Contains(buf.String(), "x.f90") || !strings.Contains(buf.String(), "empty file") {
		t.Errorf("table %q", buf.String())
	}
}
