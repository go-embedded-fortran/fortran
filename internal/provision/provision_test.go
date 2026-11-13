package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMicromambaFor(t *testing.T) {
	for _, p := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}} {
		a, err := MicromambaFor(p[0], p[1])
		if err != nil || len(a.SHA256) != 64 || !strings.Contains(a.URL, MicromambaVersion) {
			t.Errorf("%v: %+v %v", p, a, err)
		}
	}
	if _, err := MicromambaFor("windows", "amd64"); err == nil {
		t.Error("windows accepted")
	}
	if _, err := FlangFor("darwin", "arm64"); err == nil {
		t.Error("flang for macOS accepted")
	}
	if a, err := FlangFor("linux", "amd64"); err != nil || len(a.SHA256) != 64 {
		t.Errorf("%+v %v", a, err)
	}
}

func TestCondaArgs(t *testing.T) {
	got := strings.Join(CondaArgs("/e"), " ")
	for _, w := range []string{"gfortran==15.2.0", "--override-channels", "-c conda-forge", "-p /e"} {
		if !strings.Contains(got, w) {
			t.Errorf("%q lacks %q", got, w)
		}
	}
}

func TestParseContentRange(t *testing.T) {
	f, l, s, err := parseContentRange("bytes 0-9/100")
	if err != nil || f != 0 || l != 9 || s != 100 {
		t.Fatal(f, l, s, err)
	}
	for _, in := range []string{"", "bytes 0-9/*", "bytes 5-4/10", "bytes 0-10/10", "items 0-1/2", "bytes a-b/c", "bytes 0-1", "bytes 1/2"} {
		if _, _, _, err := parseContentRange(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func FuzzParseContentRange(f *testing.F) {
	for _, s := range []string{"bytes 0-9/100", "bytes 0-9/*", "", "bytes -1-2/3", "bytes 9223372036854775807-9223372036854775807/9223372036854775807"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		first, last, size, err := parseContentRange(s)
		if err == nil && (first < 0 || first > last || last >= size) {
			t.Fatalf("%q: %d-%d/%d", s, first, last, size)
		}
	})
}

// serve answers ranges, like the release CDN.
func serve(body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x", time.Time{}, strings.NewReader(string(body)))
	})
}

func TestDownloadRangesAndSum(t *testing.T) {
	old := chunkSize
	chunkSize = 7
	defer func() { chunkSize = old }()
	body := []byte(strings.Repeat("0123456789", 5))
	srv := httptest.NewServer(serve(body))
	defer srv.Close()
	sum := sha256.Sum256(body)
	dst := filepath.Join(t.TempDir(), "d", "f")
	if err := Download(context.Background(), srv.URL, dst, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(body) {
		t.Fatal("content differs")
	}
	bad := filepath.Join(t.TempDir(), "f")
	if err := Download(context.Background(), srv.URL, bad, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong sum accepted")
	}
	if _, err := os.Stat(bad); err == nil {
		t.Fatal("wrong file kept")
	}
}

func TestDownloadFromFallsBack(t *testing.T) {
	old, oldA, oldW := chunkSize, attempts, retryWait
	attempts, retryWait = 1, time.Millisecond
	defer func() { chunkSize, attempts, retryWait = old, oldA, oldW }()
	body := []byte("hello")
	good := httptest.NewServer(serve(body))
	defer good.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", 504) }))
	defer broken.Close()
	sum := sha256.Sum256(body)
	dst := filepath.Join(t.TempDir(), "f")
	if err := DownloadFrom(context.Background(), []string{broken.URL, good.URL}, dst, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
}
