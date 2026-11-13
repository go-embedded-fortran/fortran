// Package sema is the semantic analysis of go-embedded-fortran (bib/fortran.md
// §10, milestone M2): it checks a parsed file (package syntax) against the
// rules of Fortran 2018 and annotates the tree with types, kinds, ranks,
// constant values and resolved symbols (Info). The annotated tree is the
// contract both back ends of bib §4.2 consume: the evaluator and a future
// WebAssembly lowering walk the syntax tree and ask Info; neither re-derives a
// type.
//
// LFortran's ASR (BSD-3) is the design reference; no code is ported.
//
// # Standard references (J3/18-007r1, Fortran 2018)
//
//   - 7.4.1-7.4.5  intrinsic types, kind type parameters (the kinds are those of
//     the oracle, gfortran 15.2.0), character length.
//   - 7.5  derived types: components, extends, type-bound procedure bindings,
//     forward references of pointer and allocatable components (7.5.4.1).
//   - 8.2-8.7  type declaration statements, attributes (intent, parameter,
//     dimension, pointer, ...), IMPLICIT (8.7.1 rules, 8.7.2 "implicit none").
//   - 9.4, 9.5  designators: components, array elements and sections,
//     substrings; the rank of an array section.
//   - 10.1.5.2  numeric operations (Table 10.2 gives the result type), 10.1.5.3
//     relational, 10.1.5.4 logical; 10.1.12 constant expressions, folded with
//     overflow, division by zero and a bounded character length as errors.
//   - 10.2.1  intrinsic assignment (Table 10.8: the convertible pairs).
//   - 11.1.7  DO variables may not be redefined inside the loop.
//   - 14.2.2  use association: only, rename, accessibility; 19.3-19.5 scopes,
//     host association, construct entities.
//   - 15.4.2.2 explicit interface requirements; 15.4.3.4.1-15.4.3.4.5
//     generic interfaces, defined operations and assignment, and the
//     restrictions on generic declarations (C1511, C1512, C1514); 15.5.2
//     argument association; 15.5.5.2 generic resolution; 15.8.1 elemental
//     references (call.go).
//   - 15.6.4 (deleted in Annex B) statement functions.
//
// # Leniency
//
// The analyser refuses less than gfortran does and never more on purpose: an
// expression it cannot classify yet (a defined operator before generic
// resolution, a name from a module of another file, an intrinsic absent from
// its table) takes the type Unknown, on which no check is made. The differential
// against gfortran (cmd/foracle sema-rate, README) measures both directions.
//
// # Errors and bounds
//
// Diagnostics are "file:line:col: message" (syntax.Error). Analyze never
// panics: a panic is recovered into an "internal error" diagnostic, which the
// fuzzer treats as a failure. Limits bound the symbol count, the length of a
// folded character constant, the nesting of scopes and expressions and the
// number of reported errors; a bound is a refusal, not a truncation.
package sema
