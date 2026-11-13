// Package syntax is a pure-Go (CGO_ENABLED=0) lexer and recursive-descent
// parser for Fortran, producing a typed AST with byte offsets that resolve to
// file:line:col.
//
// It is milestone M1 part 1 of go-embedded-fortran (bib/fortran.md §10). The
// grammar is hand-written: LFortran's GLR grammar (BSD-3, 378 declared
// conflicts) is a design reference for the AST (grammar/AST.asdl), not ported.
//
// # Standard references (J3/18-007r1, Fortran 2018)
//
// Source form, read in the standard's text:
//
//   - 6.3.1  a program unit is a sequence of lines; the two forms are never
//     mixed in one program unit. The form is processor dependent: here it is
//     chosen by the file extension (DetectForm: .f90 .f95 .f03 .f08 free;
//     .f .for .f77 .ftn fixed, as gfortran does) and overridden by
//     Options.Form.
//   - 6.3.2.1-6.3.2.6  free form: blanks, "!" comments, "&" continuation in a
//     non-character context and in a character context (a leading "&" is
//     required on the next line), ";" statement termination, a label before
//     any statement, at most 255 continuation lines (Limits.MaxContinuations).
//   - 6.3.3.1-6.3.3.5  fixed form: labels in columns 1-5, continuation in
//     column 6 (blank or zero starts a statement), statement in columns 7-72,
//     "C" or "*" in column 1 and "!" outside column 6 start comments, blanks
//     insignificant outside character context, ";" terminates a statement.
//   - 6.4  INCLUDE line: a single source line, no label; the included text
//     replaces the line, nests, and may not include the same source text at
//     any level (refused here as a recursive include). The interpretation of
//     the character literal is processor dependent: here a path in
//     Options.FS (an fs.FS), never the host file system by default.
//
// Statements and constructs follow clauses 7 (types), 8 (attribute
// declarations and specifications), 10 (expressions: the precedence of
// 10.1.1, defined unary operators above ** above * and / above unary and
// binary + and - above // above relational above .NOT. above .AND. above
// .OR. above .EQV. and .NEQV. above defined binary operators), 11 (execution
// control), 12 (input/output statements; FORMAT contents are kept as text),
// and 14 (program units). Deleted features of Annex B that the F77 suites use
// (arithmetic IF, labelled DO with a shared terminal statement, computed GO
// TO) are parsed.
//
// Fixed form has no reserved words (bib §11): after the blanks are removed
// the statement is classified (assignment, DO, keyword statement) and blanks
// are re-inserted after its keywords, so that one token-based parser serves
// both forms; "DO 10 I = 1.10" is an assignment.
//
// # Errors and bounds
//
// Errors are reported as "file:line:col: message", the parser recovers at the
// next statement and never panics. Limits bound the source size (also summed
// over includes), the nesting depth of constructs and expressions, the include
// depth, the continuation lines of a statement and the number of reported
// errors; a bound is a refusal with an error, not a truncation.
package syntax
