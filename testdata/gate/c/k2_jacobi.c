/* K2 of bib/fortran.md 4.2, line for line from ../k2_jacobi.f90: column-major
   u(i,j) is u[(i-1) + (j-1)*n]; j outer, i inner. -DN=... -DSWEEPS=... override
   the size (the smoke run). */
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

#ifndef N
#define N 1024
#endif
#ifndef SWEEPS
#define SWEEPS 100
#endif

static double now(void) {
  struct timespec t;
  clock_gettime(CLOCK_MONOTONIC, &t);
  return (double)t.tv_sec + (double)t.tv_nsec * 1e-9;
}

#define U(i, j) u[((i) - 1) + ((long)(j) - 1) * n]
#define V(i, j) v[((i) - 1) + ((long)(j) - 1) * n]

int main(void) {
  const int n = N;
  double *u = malloc(sizeof(double) * (size_t)n * n);
  double *v = malloc(sizeof(double) * (size_t)n * n);
  if (!u || !v) return 1;
  for (int j = 1; j <= n; j++)
    for (int i = 1; i <= n; i++) U(i, j) = (double)((i * 7 + j * 13) % 17) / 17.0;
  for (long k = 0; k < (long)n * n; k++) v[k] = u[k];
  double t0 = now();
  for (int s = 1; s <= SWEEPS; s++) {
    for (int j = 2; j <= n - 1; j++)
      for (int i = 2; i <= n - 1; i++)
        V(i, j) = 0.25 * (U(i - 1, j) + U(i + 1, j) + U(i, j - 1) + U(i, j + 1));
    for (int j = 2; j <= n - 1; j++)
      for (int i = 2; i <= n - 1; i++) U(i, j) = V(i, j);
  }
  double t1 = now();
  double sum = 0.0;
  for (long k = 0; k < (long)n * n; k++) sum += u[k];
  printf("%.16E\n", sum);
  printf("time %.6f\n", t1 - t0);
  return 0;
}
