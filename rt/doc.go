// Package rt is the Fortran runtime's formatted and list-directed input and
// output, in pure Go: the part of M4 that sits under print, write and read
// (bib/fortran.md §5, rows 1 and 2). Contender A (package eval) calls it
// directly; contender B will call the same functions as host functions.
//
// # What it implements
//
// F2018 12.6 data transfer statements (formatted, list-directed, internal
// files, advancing and non-advancing output), 12.5 units (an in-memory unit
// table: 5 standard input, 6 standard output, 0 standard error, read-only files
// from an fs.FS, in-memory scratch files with rewind and backspace), 12.11
// error, end-of-file and end-of-record conditions (IOSTAT values are
// libgfortran's: -1, -2, 5006, 5010, 5011), 13.2 to 13.4 format specification
// and format control (repeat counts, nested groups, unlimited repetition,
// reversion), 13.7 data edit descriptors (I, B, O, Z, F, E, D, EN, ES, G, L, A;
// EX is implemented but off, see below), 13.8 control edit descriptors (X, T,
// TL, TR, /, :, kP, S, SP, SS, BN, BZ, RN, RU, RD, RZ, RC, RP, DC, DP), 13.9
// character string edit descriptors (quoted strings and nH), and 13.10
// list-directed input and output.
//
// # Where it comes from
//
// The algorithms are LLVM flang-rt's (llvmorg-23.1.4, Apache-2.0 WITH
// LLVM-exception, see NOTICE): edit-output.cpp (EditIntegerOutput,
// EditBOZOutput, EditEorDOutput, EditFOutput, EditForGOutput, FormatExponent,
// EditEXOutput) is ported into real.go and output.go; format-implementation.h
// (CueUpNextDataEdit, GetNextDataEdit) into format.go and control.go; the input
// side (input.go, listin.go) follows edit-input.cpp and io-stmt.cpp's
// list-directed state machine. Float to decimal conversion is not flang's
// decimal library: it is strconv's correctly rounded shortest and fixed
// algorithms for the nearest modes, and the exact decimal expansion of the
// binary value (decimal.go) for RU, RD, RZ and RC. TestDecimalAgainstBig proves
// the digits against math/big.
//
// # gfortran is the equality oracle
//
// Where flang-rt and gfortran 15.2.0 print different text, rt prints
// gfortran's, because that is what a notebook's `print *` shows. The
// differences found by the differential tests (rt/diff_test.go and
// rt/diffin_test.go, which compile and run generated programs with the pinned
// gfortran) are ported as gfortran does them and listed here:
//
//   - Fw.d with w = 0 prints no leading zero (".50"), Ew.d, Dw.d, ESw.d and
//     ENw.d with w = 0 print the exponent field of list-directed output
//     (E+00 for REAL(4), E+000 for REAL(8)) and a leading zero; F0 is F0.0;
//     E and D need a d unless w = 0 (and then a finite value is refused);
//     E, D and G refuse d = 0 (iostat 5006), G0 is G with 9 or 17 digits.
//   - G editing decides between the F and the E form from the shortest
//     decimal representation of the value rounded half away from zero to d
//     digits, not from the exact binary value: G8.1 of 0.95 is "1.".
//   - I, B, O, Z with m > w are refused; O and Z of the most negative INTEGER(8)
//     with m = 0 print a blank field.
//   - A on an INTEGER or LOGICAL item writes its bytes, least significant first;
//     L on an INTEGER is non-zero = T; Aw with w = 0 is refused.
//   - A format needs commas between edit descriptors (not around /, : and
//     after kP); characters after the final parenthesis are ignored.
//   - EX is refused (iostat 5006) as gfortran 15.2 does. EnableEX turns the
//     flang-rt implementation on; it is not covered by the differential.
//   - Directional rounding (RU, RD) looks at d+20 decimals of an F field only.
//   - List-directed REAL is G16.9E2 (G25.17E3 for REAL(8)) with a leading
//     blank, the E form being ES; a COMPLEX is "(re,im)" right-justified in
//     2w+3 with one more digit in the E form; adjacent character values
//     are not separated.
//   - After an error in the middle of an output statement the record written so
//     far stays, without its newline, as libgfortran leaves it.
//   - Reading: Inf, NaN and out-of-range values are delivered, not refused
//     (1e400 is +Inf); a lone sign is zero; "E+" followed by blanks is a zero
//     exponent; a repeat count r*c delivers its value only to items of the
//     kind of the first (libgfortran refuses the others, iostat 5010).
//
// Known differences that rt keeps (counted by the tests, never hidden): EN with
// many digits (REAL(4) with d >= 6 at a carry boundary, d >= 16): gfortran
// rounds to d+1 digits and pads with zeros; E, D, G with d = 17 of REAL(4) zero
// omit the leading zero; BZ with an exponent letter or a lone sign; a ';' without
// DC; a COMPLEX list-directed value cut short, whose real part gfortran keeps.
//
// # Bounds
//
// A format, a record and a statement are bounded before anything is allocated:
// MaxFormatLen, MaxFormatDepth, MaxRepeat, MaxFieldWidth, MaxDigits, a limit of
// 2^24 format items per statement, DefaultRecLen (a unit's RecLen overrides),
// MaxFileSize. Each is a refusal (an *Error with the libgfortran IOSTAT), never a
// panic or an unbounded loop; the fuzz targets FuzzFormat and FuzzInput run the
// parsers and converters on arbitrary text.
//
// # Allocations
//
// A statement on a warm unit allocates nothing (the statement state, the format
// control stack and the digit buffer live in the Unit; formats are compiled once
// and cached by string). A Unit is not safe for concurrent use.
package rt
