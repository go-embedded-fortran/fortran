package gate

import (
	"errors"
	"math"
	"testing"
)

func home(name string, ratio float64) Home {
	return Home{Name: name, Run: true, GA: ratio, GB0: 1}
}

func TestThresholdsArePinned(t *testing.T) {
	if SwitchToB != 5 || AllowA != 2 || ArrayWithin != 1.2 {
		t.Fatalf("thresholds are %v %v %v; bib/fortran.md 4.2 says 5, 2, 1.2", SwitchToB, AllowA, ArrayWithin)
	}
}

func TestDecide(t *testing.T) {
	notRun := Home{Name: "playground"}
	for _, c := range []struct {
		name string
		in   Inputs
		want Decision
	}{
		// the 5 edge: >= 5 in either home is B, even when the other says A
		{"desktop exactly 5", Inputs{home("desktop", 5), home("playground", 1), 1, 1}, B},
		{"desktop just under 5", Inputs{home("desktop", math.Nextafter(5, 0)), home("playground", 1), 1, 1}, AFirst},
		{"playground exactly 5", Inputs{home("desktop", 1), home("playground", 5), 1, 1}, B},
		{"playground far above 5", Inputs{home("desktop", 1), home("playground", 40), 1, 1}, B},
		{"B wins over bad arrays", Inputs{home("desktop", 7), home("playground", 7), 9, 9}, B},
		{"desktop alone at 5", Inputs{home("desktop", 5), notRun, 1, 1}, B},
		// the 2 edge, both homes, arrays fine
		{"both exactly 2", Inputs{home("desktop", 2), home("playground", 2), 1, 1}, A},
		{"desktop just above 2", Inputs{home("desktop", math.Nextafter(2, 3)), home("playground", 1), 1, 1}, AFirst},
		{"playground just above 2", Inputs{home("desktop", 1), home("playground", math.Nextafter(2, 3)), 1, 1}, AFirst},
		{"both far below 2", Inputs{home("desktop", 0.3), home("playground", 0.5), 1, 1}, A},
		// the 1.2 edge
		{"K4 exactly 1.2", Inputs{home("desktop", 1), home("playground", 1), 1.2, 1}, A},
		{"matmul exactly 1.2", Inputs{home("desktop", 1), home("playground", 1), 1, 1.2}, A},
		{"K4 just above 1.2", Inputs{home("desktop", 1), home("playground", 1), math.Nextafter(1.2, 2), 1}, AFirst},
		{"matmul just above 1.2", Inputs{home("desktop", 1), home("playground", 1), 1, math.Nextafter(1.2, 2)}, AFirst},
		// between
		{"between", Inputs{home("desktop", 3), home("playground", 4), 1, 1}, AFirst},
		// one home only: "in both homes" is unmet, the rule cannot pick A
		{"desktop only, would be A", Inputs{home("desktop", 1), notRun, 1, 1}, AFirst},
		{"playground only, would be A", Inputs{notRun, home("playground", 1), 1, 1}, AFirst},
		// arrays not measured
		{"arrays unmeasured", Inputs{home("desktop", 1), home("playground", 1), 0, 0}, AFirst},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, err := Decide(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if v.Decision != c.want {
				t.Errorf("decision %v, want %v; reasons %q", v.Decision, c.want, v.Reasons)
			}
			if len(v.Reasons) == 0 {
				t.Error("no reasons")
			}
		})
	}
}

func TestDecideRatioIsOfGeometricMeans(t *testing.T) {
	// g(rA) = 10, g(rB0) = 2: ratio 5 -> B, through Home.Ratio, not through a stored ratio.
	v, err := Decide(Inputs{Desktop: Home{Name: "desktop", Run: true, GA: 10, GB0: 2}})
	if err != nil || v.Decision != B {
		t.Fatalf("%v %v", v, err)
	}
}

func TestDecideRefusals(t *testing.T) {
	if _, err := Decide(Inputs{}); !errors.Is(err, ErrNoInput) {
		t.Errorf("no home: %v", err)
	}
	for _, h := range []Home{
		{Name: "desktop", Run: true, GA: 0, GB0: 1},
		{Name: "desktop", Run: true, GA: 1, GB0: 0},
		{Name: "desktop", Run: true, GA: math.NaN(), GB0: 1},
		{Name: "desktop", Run: true, GA: 1, GB0: math.Inf(1)},
	} {
		if _, err := Decide(Inputs{Desktop: h}); err == nil {
			t.Errorf("%+v accepted", h)
		}
	}
}
