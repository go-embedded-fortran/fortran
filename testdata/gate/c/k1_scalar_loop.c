/* K1 of bib/fortran.md 4.2, line for line from ../k1_scalar_loop.f90.
   Line 1: checksum. Line 2: "time <seconds>" (monotonic clock around the loop).
   -DN=... overrides the size (the smoke run). */
#include <stdio.h>
#include <time.h>

#ifndef N
#define N 100000000
#endif

static double now(void) {
  struct timespec t;
  clock_gettime(CLOCK_MONOTONIC, &t);
  return (double)t.tv_sec + (double)t.tv_nsec * 1e-9;
}

int main(void) {
  long long n = N;
  double s = 0.0;
  double t0 = now();
  for (long long i = 1; i <= n; i++) {
    double x = (double)i;
    s = s + 1.0 / (x * x);
  }
  double t1 = now();
  printf("%.16E\n", s);
  printf("time %.6f\n", t1 - t0);
  return 0;
}
