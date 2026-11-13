/* K4 a = b*c + d of bib/fortran.md 4.2 as B delivers it: the whole-array
   expression is a host import (go-ndarray), with the operands copied out of
   linear memory and the result copied back inside the timed region. Built for
   wasm32-wasip1 only; the host module is "fortran_gate".
   -DN=... -DREPS=... override the size (the smoke run). */
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

#ifndef N
#define N 10000000
#endif
#ifndef REPS
#define REPS 10
#endif

__attribute__((import_module("fortran_gate"), import_name("muladd")))
extern void host_muladd(double *a, const double *b, const double *c, const double *d, int n);

static double now(void) {
  struct timespec t;
  clock_gettime(CLOCK_MONOTONIC, &t);
  return (double)t.tv_sec + (double)t.tv_nsec * 1e-9;
}

int main(void) {
  const int n = N;
  double *a = malloc(sizeof(double) * (size_t)n);
  double *b = malloc(sizeof(double) * (size_t)n);
  double *c = malloc(sizeof(double) * (size_t)n);
  double *d = malloc(sizeof(double) * (size_t)n);
  if (!a || !b || !c || !d) return 1;
  for (int i = 1; i <= n; i++) {
    b[i - 1] = (double)(i % 13) / 13.0;
    c[i - 1] = (double)(i % 11) / 11.0;
    d[i - 1] = (double)(i % 7) / 7.0;
  }
  double t0 = now();
  for (int r = 1; r <= REPS; r++) {
    b[0] = (double)r;
    host_muladd(a, b, c, d, n);
  }
  double t1 = now();
  double sum = 0.0;
  for (int i = 0; i < n; i++) sum += a[i];
  printf("%.16E\n", sum);
  printf("time %.6f\n", t1 - t0);
  return 0;
}
