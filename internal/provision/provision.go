// Package provision installs the pinned oracle toolchain into a home
// directory, without root.
//
// bib/fortran.md leaves open how to obtain gfortran 15.2.0 on macOS arm64 and
// Linux amd64; the choice made here, and why:
//
//   - Distributions do not agree on 15.2.0 (Homebrew's gcc is 16.x today,
//     Ubuntu's gfortran-15 needs root), pkgx has no pinned build, and building
//     GCC from source takes an hour per lane. conda-forge publishes
//     gfortran 15.2.0 for both platforms as relocatable packages.
//   - micromamba is one static binary per platform, published with a sha256 by
//     its GitHub release, which this package pins and checks. It installs the
//     conda-forge package `gfortran==15.2.0` into <home>/env; micromamba itself
//     verifies the packages against the channel's repodata checksums. The
//     version of the result is checked again by oracle.Verify, which is the
//     real gate: anything that is not 15.2.0 is refused.
//
// Layout of the home directory (see oracle.HomeGfortran, oracle.HomeFlang):
//
//	bin/micromamba   the pinned micromamba
//	mamba/           its root prefix (package cache)
//	env/             the conda environment: bin/gfortran, lib/libgfortran…
//	dl/<asset>       the downloaded flang archive (--flang)
//	llvm/            the extracted LLVM 23.1.4 release, bin/flang (--flang)
//
// Every step is skipped when its output exists, so a rerun repairs a home.
// The macOS gfortran links through the Xcode command line tools' SDK, which
// must be installed (xcode-select --install).
package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
)

// MicromambaVersion is the pinned micromamba release tag.
const MicromambaVersion = "2.10.0-0"

// Asset is a release file, the sha256 GitHub publishes for it, and its REST
// API URL (the second address of the same file).
type Asset struct {
	Name   string
	SHA256 string
	URL    string
	APIURL string
}

// MicromambaFor returns the pinned micromamba binary for a GOOS/GOARCH.
func MicromambaFor(goos, goarch string) (Asset, error) {
	const base = "https://github.com/mamba-org/micromamba-releases/releases/download/" + MicromambaVersion + "/"
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return Asset{"micromamba-osx-arm64", "c547639f5caf742236641c362746abb4a3505774753ecd08fe341643670c4c01",
			base + "micromamba-osx-arm64", "https://api.github.com/repos/mamba-org/micromamba-releases/releases/assets/679631413"}, nil
	case "linux/amd64":
		return Asset{"micromamba-linux-64", "701f39c06f8e6a670a0e186c8479b9e5c0502a2f0cda6cef65d83bdd2875e8bd",
			base + "micromamba-linux-64", "https://api.github.com/repos/mamba-org/micromamba-releases/releases/assets/679631392"}, nil
	}
	return Asset{}, fmt.Errorf("provision: no pinned gfortran %s toolchain for %s/%s (macOS arm64 and Linux amd64 only)", oracle.GfortranVersion, goos, goarch)
}

// FlangFor returns the pinned LLVM release archive that holds flang. The
// macOS release has no flang (Release.cmake appends it for non-Darwin hosts
// only), so only Linux amd64 has one.
func FlangFor(goos, goarch string) (Asset, error) {
	if goos+"/"+goarch == "linux/amd64" {
		return Asset{"LLVM-23.1.4-Linux-X64.tar.zst", "efdd1dbd089b99bfcfcde7b8eb1236163f2e88df5a981cca4f783ec9cb5e0378",
			"https://github.com/llvm/llvm-project/releases/download/llvmorg-" + oracle.FlangVersion + "/LLVM-23.1.4-Linux-X64.tar.zst",
			"https://api.github.com/repos/llvm/llvm-project/releases/assets/659252374"}, nil
	}
	return Asset{}, fmt.Errorf("provision: no flang %s release for %s/%s (Linux amd64 only)", oracle.FlangVersion, goos, goarch)
}

// Options configure Install.
type Options struct {
	Home string
	Log  io.Writer
	// Flang also installs flang 23.1.4 (Linux amd64 only).
	Flang bool
	// BaseURL, when set, replaces the download URL of every asset with
	// BaseURL/<asset name> (tests).
	BaseURL string
}

// FileSHA256 returns the hex sha256 of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Download parameters. GitHub's release CDN has answered whole-file GETs
// with 504 for hours while serving byte ranges of the same file
// (go-embedded-c/c, 2026-11-08), so archives are fetched in ranges, each
// retried. The sha256 check at the end is what guarantees the bytes.
var (
	chunkSize  int64 = 64 << 20
	attempts         = 10
	retryWait        = time.Second
	maxWait          = 30 * time.Second
	maxArchive int64 = 8 << 30 // the LLVM archive is under 2 GiB
)

// DownloadFrom is Download from the first of urls that delivers the file.
func DownloadFrom(ctx context.Context, urls []string, dst, want string) error {
	var errs []error
	for _, u := range urls {
		err := Download(ctx, u, dst, want)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Download fetches url into dst and checks its sha256; a file already there
// with the right sum is kept. A wrong sum deletes the file and is an error.
func Download(ctx context.Context, url, dst, want string) error {
	if got, err := FileSHA256(dst); err == nil && got == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	err = fetch(ctx, url, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	got, err := FileSHA256(dst)
	if err != nil {
		return err
	}
	if got != want {
		os.Remove(dst)
		return fmt.Errorf("provision: %s has sha256 %s, want %s", url, got, want)
	}
	return nil
}

// fetch writes the body of url into f, range by range.
func fetch(ctx context.Context, url string, f *os.File) error {
	total := int64(-1)
	for off := int64(0); total < 0 || off < total; {
		n, size, err := fetchRange(ctx, url, f, off)
		if err != nil {
			return err
		}
		if size < 0 {
			return nil // the server sent the whole file
		}
		if total >= 0 && size != total {
			return fmt.Errorf("provision: GET %s: the size changed from %d to %d", url, total, size)
		}
		total = size
		off += n
	}
	return nil
}

// errPermanent marks a failure a retry cannot fix.
type errPermanent struct{ error }

// fetchRange writes the range of url starting at off into f, retrying a
// server error or a broken transfer. It returns the bytes written and the
// file's total size, or -1 when the server ignored the range and sent the
// whole file from offset 0.
func fetchRange(ctx context.Context, url string, f *os.File, off int64) (int64, int64, error) {
	var err error
	wait := retryWait
	for i := range attempts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return 0, 0, ctx.Err()
			case <-time.After(wait):
			}
			wait = min(2*wait, maxWait)
		}
		var n, size int64
		n, size, err = fetchRangeOnce(ctx, url, f, off)
		if err == nil {
			return n, size, nil
		}
		if p, ok := err.(errPermanent); ok {
			return 0, 0, p.error
		}
	}
	return 0, 0, fmt.Errorf("%w (offset %d, %d attempts)", err, off, attempts)
}

func fetchRangeOnce(ctx context.Context, url string, f *os.File, off int64) (int64, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, errPermanent{err}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+chunkSize-1))
	// The REST API's asset URL serves the bytes only when asked for them;
	// the download URL ignores it.
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, err // the network: retried
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK && off == 0:
		// No range support: the whole file, bounded.
		n, err := io.Copy(io.NewOffsetWriter(f, 0), io.LimitReader(resp.Body, maxArchive+1))
		if err == nil && n > maxArchive {
			return 0, 0, errPermanent{fmt.Errorf("provision: GET %s: larger than %d bytes", url, maxArchive)}
		}
		return n, -1, err
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode >= 500:
		return 0, 0, fmt.Errorf("provision: GET %s: %s", url, resp.Status)
	default:
		return 0, 0, errPermanent{fmt.Errorf("provision: GET %s: %s", url, resp.Status)}
	}
	first, last, size, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil || first != off || size > maxArchive {
		return 0, 0, errPermanent{fmt.Errorf("provision: GET %s: Content-Range %q for offset %d", url, resp.Header.Get("Content-Range"), off)}
	}
	want := last - first + 1
	n, err := io.Copy(io.NewOffsetWriter(f, off), io.LimitReader(resp.Body, want))
	if err == nil && n != want {
		err = fmt.Errorf("provision: GET %s: %d bytes of %d at offset %d", url, n, want, off)
	}
	return n, size, err
}

// parseContentRange parses "bytes first-last/size" (RFC 9110 §14.4), with
// first <= last < size; the unknown size "*" is refused.
func parseContentRange(s string) (first, last, size int64, err error) {
	rest, ok := strings.CutPrefix(s, "bytes ")
	if !ok {
		return 0, 0, 0, fmt.Errorf("content-range %q: no bytes unit", s)
	}
	span, total, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, 0, 0, fmt.Errorf("content-range %q: no size", s)
	}
	a, b, ok := strings.Cut(span, "-")
	if !ok {
		return 0, 0, 0, fmt.Errorf("content-range %q: no range", s)
	}
	for _, p := range []struct {
		s string
		v *int64
	}{{a, &first}, {b, &last}, {total, &size}} {
		v, err := strconv.ParseInt(p.s, 10, 64)
		if err != nil || v < 0 {
			return 0, 0, 0, fmt.Errorf("content-range %q: bad number %q", s, p.s)
		}
		*p.v = v
	}
	if first > last || last >= size {
		return 0, 0, 0, fmt.Errorf("content-range %q: inconsistent", s)
	}
	return first, last, size, nil
}

func run(ctx context.Context, log io.Writer, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, log, log
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("provision: %s %v: %w", name, args, err)
	}
	return nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// extract unpacks a .tar.zst into dir, stripping the top directory. The zstd
// window of the LLVM archives is 1 GiB, hence --long=31.
func extract(ctx context.Context, archive, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	zs := exec.CommandContext(ctx, "zstd", "-d", "-q", "--long=31", "-c", archive)
	tar := exec.CommandContext(ctx, "tar", "-x", "--strip-components=1", "-C", dir)
	pr, pw := io.Pipe()
	zs.Stdout, tar.Stdin = pw, pr
	zs.Stderr, tar.Stderr = os.Stderr, os.Stderr
	if err := tar.Start(); err != nil {
		return err
	}
	if err := zs.Start(); err != nil {
		pw.Close()
		tar.Wait()
		return err
	}
	zerr := zs.Wait()
	pw.Close()
	terr := tar.Wait()
	pr.Close()
	if zerr != nil {
		return fmt.Errorf("provision: zstd: %w", zerr)
	}
	if terr != nil {
		return fmt.Errorf("provision: tar: %w", terr)
	}
	return nil
}

// CondaArgs are the micromamba arguments that create the gfortran
// environment at env: only conda-forge, exactly the pinned version.
func CondaArgs(env string) []string {
	return []string{"create", "-y", "-q", "-p", env, "--override-channels", "-c", "conda-forge",
		"gfortran==" + oracle.GfortranVersion}
}

// Install provisions o.Home.
func Install(ctx context.Context, o Options) error {
	log := o.Log
	if log == nil {
		log = io.Discard
	}
	home, err := filepath.Abs(o.Home)
	if err != nil {
		return err
	}
	o.Home = home
	url := func(a Asset) []string {
		if o.BaseURL != "" {
			return []string{o.BaseURL + "/" + a.Name}
		}
		return []string{a.URL, a.APIURL}
	}
	gf := oracle.HomeGfortran(o.Home)
	if exists(gf) {
		fmt.Fprintln(log, "provision: gfortran already installed")
	} else {
		mm, err := MicromambaFor(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return err
		}
		bin := filepath.Join(o.Home, "bin", "micromamba")
		fmt.Fprintf(log, "provision: downloading %s %s\n", mm.Name, MicromambaVersion)
		if err := DownloadFrom(ctx, url(mm), bin, mm.SHA256); err != nil {
			return err
		}
		if err := os.Chmod(bin, 0o755); err != nil {
			return err
		}
		fmt.Fprintf(log, "provision: installing gfortran %s from conda-forge\n", oracle.GfortranVersion)
		if err := run(ctx, log, o.Home, []string{"MAMBA_ROOT_PREFIX=" + filepath.Join(o.Home, "mamba")},
			bin, CondaArgs(filepath.Join(o.Home, "env"))...); err != nil {
			return err
		}
	}
	if o.Flang {
		fl := oracle.HomeFlang(o.Home)
		if exists(fl) {
			fmt.Fprintln(log, "provision: flang already installed")
			return nil
		}
		asset, err := FlangFor(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return err
		}
		archive := filepath.Join(o.Home, "dl", asset.Name)
		fmt.Fprintf(log, "provision: downloading %s\n", asset.Name)
		if err := DownloadFrom(ctx, url(asset), archive, asset.SHA256); err != nil {
			return err
		}
		fmt.Fprintln(log, "provision: extracting")
		if err := extract(ctx, archive, filepath.Join(o.Home, "llvm")); err != nil {
			return err
		}
		if !exists(fl) {
			return fmt.Errorf("provision: %s has no %s (the bib read flang's presence from Release.cmake, not from a listing)", asset.Name, fl)
		}
	}
	return nil
}
