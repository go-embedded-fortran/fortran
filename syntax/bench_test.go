package syntax_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/go-embedded-fortran/fortran/internal/corpus"
	"github.com/go-embedded-fortran/fortran/syntax"
)

// BenchmarkParseTestdata parses every file of testdata in turn.
func BenchmarkParseTestdata(b *testing.B) {
	var files [][]byte
	var names []string
	total := 0
	for _, dir := range []string{"programs", "gate"} {
		m, _ := filepath.Glob(filepath.Join("..", "testdata", dir, "*.f90"))
		for _, f := range m {
			src, err := os.ReadFile(f)
			if err != nil {
				b.Fatal(err)
			}
			files, names, total = append(files, src), append(names, f), total+len(src)
		}
	}
	b.SetBytes(int64(total))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for k, src := range files {
			if _, err := syntax.Parse(names[k], src, nil); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkParseCorpus parses the largest files of the corpora below
// $FORACLE_CORPORA that parse cleanly; the throughput is MB/s of source
// (compare with `foracle parse-bench`, which sets it against gfortran).
func BenchmarkParseCorpus(b *testing.B) {
	root := os.Getenv(envCorpora)
	if root == "" {
		b.Skipf("$%s is not set", envCorpora)
	}
	type big struct {
		name string
		src  []byte
		fsys string
	}
	var all []big
	for _, c := range floors {
		for _, d := range c.dirs {
			dir := filepath.Join(root, filepath.FromSlash(d))
			if !dirExists(dir) {
				continue
			}
			es, err := corpus.Load(dir)
			if err != nil {
				b.Fatal(err)
			}
			for _, e := range es {
				all = append(all, big{e.Path, e.Source, filepath.Dir(filepath.Join(dir, e.Path))})
			}
			break
		}
	}
	sort.Slice(all, func(i, j int) bool { return len(all[i].src) > len(all[j].src) })
	n := 0
	for _, f := range all {
		if n == 6 {
			break
		}
		if _, err := syntax.Parse(filepath.Base(f.name), f.src, &syntax.Options{FS: os.DirFS(f.fsys)}); err != nil {
			continue
		}
		n++
		f := f
		b.Run(filepath.Base(f.name), func(b *testing.B) {
			opts := &syntax.Options{FS: os.DirFS(f.fsys)}
			b.SetBytes(int64(len(f.src)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				syntax.Parse(filepath.Base(f.name), f.src, opts)
			}
		})
	}
}
