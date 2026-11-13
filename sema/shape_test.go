package sema

import "testing"

func prog(decl, body string) string {
	return "program p\n implicit none\n" + decl + body + "end\n"
}

// shapeNegatives are programs F2018 10.1.5, 10.2.1.2 and 16.9 refuse for the
// shapes of their arrays; oracle_test.go checks each against gfortran.
var shapeNegatives = []struct{ name, src, want string }{
	{"operands of different extents", prog(" real :: a(3), b(4)\n", " a = a + b\n"), "not conformable"},
	{"operands of different ranks", prog(" real :: a(3), b(3, 3)\n", " a = a + b(:, 1)\n b = b + a\n"), "not conformable"},
	{"assignment of different extents", prog(" real :: a(3), b(4)\n", " a = b\n"), "Different shape"},
	{"assignment of a section", prog(" real :: a(3), b(4)\n", " a(1:3) = b(1:2)\n"), "Different shape"},
	{"strided section", prog(" real :: a(5)\n", " a(1:5:2) = [1., 2.]\n"), "Different shape"},
	{"section of a matrix", prog(" real :: a(3, 4), v(4)\n", " v = a(1:2, 1)\n"), "Different shape"},
	{"initialisation", prog(" real :: a(3) = [1., 2.]\n", ""), "Different shape"},
	{"named constant", prog(" integer, parameter :: a(3) = [1, 2]\n", ""), "Different shape"},
	{"array constructor", prog(" real :: a(3)\n", " a = [1., 2., 3., 4.]\n"), "Different shape"},
	{"implied do", prog(" real :: a(3)\n integer :: i\n", " a = [(real(i), i = 1, 4)]\n"), "Different shape"},
	{"reshape without enough elements", prog(" integer :: m(2, 2)\n", " m = reshape([1, 2, 3], [2, 2])\n"), "not enough elements"},
	{"reshape with a named shape", prog(" integer, parameter :: sh(2) = [2, 3]\n integer :: m(2, 3)\n", " m = reshape([1, 2, 3, 4], sh)\n"), "not enough elements"},
	{"reshape result", prog(" integer :: m(2, 2)\n", " m = reshape([1, 2, 3, 4, 5, 6], [2, 3])\n"), "Different shape"},
	{"matmul inner extents", prog(" real :: a(2, 3), b(4, 2), c(2, 2)\n", " c = matmul(a, b)\n"), "MATMUL"},
	{"matmul result", prog(" real :: a(2, 3), b(3, 2), c(3, 3)\n", " c = matmul(a, b)\n"), "Different shape"},
	{"transpose of a vector", prog(" real :: a(3), b(3, 3)\n", " b = transpose(a)\n"), "rank 2"},
	{"transpose result", prog(" real :: a(2, 3), b(2, 3)\n", " b = transpose(a)\n"), "Different shape"},
	{"dot_product extents", prog(" real :: a(3), b(4), s\n", " s = dot_product(a, b)\n"), "Different shape"},
	{"spread result", prog(" real :: v(3), m(3, 4)\n", " m = spread(v, 2, 5)\n"), "Different shape"},
	{"sum with dim", prog(" real :: m(3, 4), v(3)\n", " v = sum(m, dim=1)\n"), "Different shape"},
	{"sum with a mask of other extents", prog(" real :: a(3), s\n logical :: l(4)\n", " s = sum(a, mask=l)\n"), "Different shape"},
	{"sum with a mask of another rank", prog(" real :: a(3), s\n logical :: l(3, 1)\n", " s = sum(a, mask=l)\n"), "Incompatible ranks"},
	{"elemental intrinsic extents", prog(" real :: a(3), b(4), c(3)\n", " c = max(a, b)\n"), "Different shape"},
	{"where mask extents", prog(" real :: a(3)\n logical :: l(4)\n", " where (l) a = 1.0\n"), "Different shape"},
	{"shape of a matrix", prog(" integer :: s(3), m(2, 2)\n", " s = shape(m)\n"), "Different shape"},
	{"too few elements for a dummy", "subroutine s(x)\n real :: x(5)\nend\n" + prog(" real :: a(10)\n external s\n", " call s(a(1:2))\n"), "too few elements"},
}

// shapePositives are programs both gfortran and the analyser accept: shapes the
// analyser cannot or need not compare, and the conformable cases.
var shapePositives = []struct{ name, src string }{
	{"conformable", prog(" real :: a(3), b(3), c(3, 4)\n", " a = a + b\n a = 2.0 * b\n c = 1.0\n c(:, 1) = a\n")},
	{"sections", prog(" real :: a(10), b(5)\n", " b = a(1:10:2)\n b = a(6:10)\n b(2:3) = a(1:2)\n")},
	{"allocatable left side", prog(" real, allocatable :: a(:)\n", " a = [1., 2., 3., 4.]\n a = [1., 2.]\n")},
	{"assumed shape", "subroutine s(a, b)\n real :: a(:), b(:)\n a = b\nend\n"},
	{"constructor with arrays", prog(" real :: a(5), b(2)\n", " a = [b, b, 1.0]\n")},
	{"implied do", prog(" real :: a(4)\n integer :: i\n", " a = [(real(i), i = 1, 4)]\n a = [(real(i), i = 1, 2), (real(i), i = 1, 2)]\n")},
	{"matmul and transpose", prog(" real :: a(2, 3), b(3, 2), c(2, 2), t(3, 2), v(2)\n", " c = matmul(a, b)\n t = transpose(a)\n v = matmul(a, [1., 2., 3.])\n v = matmul([1., 2., 3.], b)\n")},
	{"reshape with padding", prog(" integer :: m(2, 3)\n", " m = reshape([1, 2, 3], [2, 3], [0])\n m = reshape([1, 2, 3, 4, 5, 6], [2, 3])\n")},
	{"reductions", prog(" real :: m(3, 4), v3(3), v4(4), s\n logical :: l(3, 4)\n", " s = sum(m)\n v3 = sum(m, dim=2)\n v4 = sum(m, dim=1)\n v4 = sum(m, 1, mask=l)\n s = maxval(m, mask=l)\n")},
	{"shape, size and bounds", prog(" real :: m(3, 4)\n integer :: s(2), i, l(2)\n", " s = shape(m)\n i = size(m)\n i = size(m, 2)\n l = lbound(m)\n l = ubound(m)\n")},
	{"spread", prog(" real :: v(3), m(3, 4), n(5, 3)\n", " m = spread(v, 2, 4)\n n = spread(v, 1, 5)\n")},
	{"size as a constant", prog(" integer, parameter :: a(3) = [1, 2, 3]\n real :: b(size(a)), c(size(b))\n", " b = 1.0\n c = b\n")},
	{"implied-shape named constant", prog(" integer, parameter :: a(*) = [1, 2, 3]\n real :: b(size(a))\n", " b = 1.0\n")},
	{"elemental intrinsic", prog(" real :: a(3), b(3), c(3)\n", " c = max(a, b)\n c = max(a, 1.0)\n c = sqrt(a)\n")},
	{"where", prog(" real :: a(3), b(3)\n logical :: l(3)\n", " where (l) a = b\n where (l)\n  a = 1.0\n elsewhere\n  a = 2.0\n end where\n")},
	{"explicit-shape dummy", "subroutine s(x)\n real :: x(5)\nend\n" + prog(" real :: a(10), b(5)\n external s\n", " call s(a)\n call s(b)\n call s(a(1:5))\n call s(a(3))\n")},
	{"arrays of structures", prog(" type t\n  real :: v(3)\n end type\n type(t) :: s(4)\n real :: a(4), b(3)\n", " a = s%v(1)\n b = s(1)%v\n")},
	{"character components", prog(" type t\n  character(4) :: name\n end type\n type(t) :: s(4)\n character :: a(4)\n", " a = s%name(2:2)\n")},
}

func TestShapeNegatives(t *testing.T) {
	for _, c := range shapeNegatives {
		if c.want == "" {
			continue
		}
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.src, c.want) })
	}
}

func TestShapePositives(t *testing.T) {
	for _, c := range shapePositives {
		t.Run(c.name, func(t *testing.T) { wantOK(t, c.src) })
	}
}

// TestShapeInfo: the typed tree carries the extents the analyser computed.
func TestShapeInfo(t *testing.T) {
	info := wantOK(t, prog(" real :: a(3, 4), b(2)\n integer :: s(2)\n", " b = a(1:2, 2)\n s = shape(a)\n"))
	var gotSection, gotShape bool
	for _, tv := range info.Types {
		if tv.Rank == 1 && len(tv.Shape) == 1 && tv.Shape[0] == 2 {
			gotSection = true
		}
		if tv.Rank == 2 && len(tv.Shape) == 2 && tv.Shape[0] == 3 && tv.Shape[1] == 4 {
			gotShape = true
		}
	}
	if !gotSection || !gotShape {
		t.Fatalf("shapes missing from Info: section %v, whole array %v", gotSection, gotShape)
	}
}
