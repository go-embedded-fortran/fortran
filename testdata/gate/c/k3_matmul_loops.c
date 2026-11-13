/* K3 (triple loop, order j,k,i) of bib/fortran.md 4.2, line for line from
   ../k3_matmul_loops.f90. -DN=... overrides the size (the smoke run). */
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

#ifndef N
#define N 512
#endif

static double now(void) {
  struct timespec t;
  clock_gettime(CLOCK_MONOTONIC, &t);
  return (double)t.tv_sec + (double)t.tv_nsec * 1e-9;
}

#define A(i, j) a[((i) - 1) + ((long)(j) - 1) * n]
#define B(i, j) b[((i) - 1) + ((long)(j) - 1) * n]
#define C(i, j) c[((i) - 1) + ((long)(j) - 1) * n]

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
  for (long k = 0; k < (long)n * n; k++) c[k] = 0.0;
  double t0 = now();
  for (int j = 1; j <= n; j++)
    for (int k = 1; k <= n; k++)
      for (int i = 1; i <= n; i++) C(i, j) = C(i, j) + A(i, k) * B(k, j);
  double t1 = now();
  double sum = 0.0;
  for (long k = 0; k < (long)n * n; k++) sum += c[k];
  printf("%.16E\n", sum);
  printf("time %.6f\n", t1 - t0);
  return 0;
}
