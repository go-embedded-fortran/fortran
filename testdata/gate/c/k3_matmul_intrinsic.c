/* K3 c = matmul(a, b) of bib/fortran.md 4.2 as B delivers it: matmul is a host
   import (the Go library, go-ndarray), so the arrays are copied out of linear
   memory and the result copied back, inside the timed region (bib 4.2: "the
   time includes copying operands into linear memory and the result out").
   Built for wasm32-wasip1 only; the host module is "fortran_gate".
   -DN=... overrides the size (the smoke run). */
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

#ifndef N
#define N 512
#endif

__attribute__((import_module("fortran_gate"), import_name("matmul")))
extern void host_matmul(const double *a, const double *b, double *c, int n);

static double now(void) {
  struct timespec t;
  clock_gettime(CLOCK_MONOTONIC, &t);
  return (double)t.tv_sec + (double)t.tv_nsec * 1e-9;
}

#define A(i, j) a[((i) - 1) + ((long)(j) - 1) * n]
#define B(i, j) b[((i) - 1) + ((long)(j) - 1) * n]

int main(void) {
  const int n = N;
  double *a = malloc(sizeof(double) * (size_t)n * n);
  double *b = malloc(sizeof(double) * (size_t)n * n);
  double *c = malloc(sizeof(double) * (size_t)n * n);
  if (!a || !b || !c) return 1;
  for (int j = 1; j <= n; j++)
    for (int i = 1; i <= n; i++) {
      A(i, j) = (double)((i + 2 * j) % 7) / 7.0;
      B(i, j) = (double)((3 * i + j) % 5) / 5.0;
    }
  double t0 = now();
  host_matmul(a, b, c, n);
  double t1 = now();
  double sum = 0.0;
  for (long k = 0; k < (long)n * n; k++) sum += c[k];
  printf("%.16E\n", sum);
  printf("time %.6f\n", t1 - t0);
  return 0;
}
