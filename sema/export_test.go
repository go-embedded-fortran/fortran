package sema

// Case is a named program, shared with the external test package.
type Case struct{ Name, Src string }

// CallCases are the programs of call_test.go: those the analyser must refuse
// and those it must accept. oracle_test.go checks both lists against gfortran.
func CallCases() (neg, pos []Case) {
	for _, c := range callNegatives {
		neg = append(neg, Case{c.name, c.src})
	}
	for _, c := range callPositives {
		pos = append(pos, Case{c.name, c.src})
	}
	return neg, pos
}

// ShapeCases are the programs of shape_test.go, for the same cross-check.
func ShapeCases() (neg, pos []Case) {
	for _, c := range shapeNegatives {
		neg = append(neg, Case{c.name, c.src})
	}
	for _, c := range shapePositives {
		pos = append(pos, Case{c.name, c.src})
	}
	return neg, pos
}
