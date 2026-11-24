package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tahakocp/callshift/internal/change"
)

// Options control a scan.
type Options struct {
	MinConfidence int
	Exclude       []string // glob patterns matched against slash paths and base names
	MaxFileBytes  int64
}

// Stats describe what a scan looked at.
type Stats struct {
	Files    int           `json:"files"`
	Bytes    int64         `json:"bytes"`
	Skipped  int           `json:"skipped"`
	Duration time.Duration `json:"duration_ns"`
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, "target": true,
	".venv": true, "venv": true, "__pycache__": true, ".next": true, ".nuxt": true, "coverage": true, "third_party": true,
	"site-packages": true, ".tox": true, ".mypy_cache": true, "bower_components": true, ".terraform": true, "Pods": true}

// Scanner holds a prepared change set.
type Scanner struct {
	cs *change.Set
	m  *matcher
	ix *changeIndex
	// needles are the literal path segments of changed operations. A call
	// can only match a changed operation if its URL contains one of them,
	// so files without any needle are skipped before lexing.
	needles []string
}

// New prepares a scanner for a change set.
func New(cs *change.Set) *Scanner {
	s := &Scanner{cs: cs, m: newMatcher(cs), ix: newIndex(cs)}
	seen := map[string]bool{}
	for _, c := range cs.Changes {
		lit := false
		for _, seg := range strings.Split(c.Path, "/") {
			if seg == "" || strings.ContainsAny(seg, "{}") {
				continue
			}
			lit = true
			if !seen[seg] {
				seen[seg] = true
				s.needles = append(s.needles, seg)
			}
		}
		if !lit {
			s.needles = nil // a change on "/" or "/{id}": no safe prefilter
			break
		}
	}
	return s
}

func (s *Scanner) worthLexing(src string) bool {
	if s.needles == nil {
		return true
	}
	for _, n := range s.needles {
		if strings.Contains(src, n) {
			return true
		}
	}
	return false
}

// Source scans one in-memory file.
func (s *Scanner) Source(name, src string, minConf int) []Finding {
	if !s.worthLexing(src) {
		return nil
	}
	fs := File(name, src, s.cs, s.m, s.ix)
	out := fs[:0]
	for _, f := range fs {
		if f.Level >= minConf {
			out = append(out, f)
		}
	}
	return out
}

// Paths scans files and directories.
func (s *Scanner) Paths(roots []string, opt Options) ([]Finding, Stats, error) {
	start := time.Now()
	var st Stats
	var all []Finding
	var files, bytes, skipped int64
	if opt.MaxFileBytes == 0 {
		opt.MaxFileBytes = 2 << 20
	}
	jobs := make(chan string, 256)
	results := make(chan []Finding, 256)
	var wg sync.WaitGroup
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				b, err := os.ReadFile(p)
				if err != nil {
					atomic.AddInt64(&skipped, 1)
					continue
				}
				atomic.AddInt64(&files, 1)
				atomic.AddInt64(&bytes, int64(len(b)))
				results <- s.Source(filepath.ToSlash(p), string(b), opt.MinConfidence)
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		for fs := range results {
			all = append(all, fs...)
		}
		close(done)
	}()
	var walkErr error
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if p != root && (skipDirs[d.Name()] || excluded(rel, d.Name(), opt.Exclude)) {
					return filepath.SkipDir
				}
				return nil
			}
			if langFor(p) == nil || excluded(rel, d.Name(), opt.Exclude) || strings.HasSuffix(d.Name(), ".min.js") {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() > opt.MaxFileBytes {
				atomic.AddInt64(&skipped, 1)
				return nil
			}
			jobs <- p
			return nil
		})
		if err != nil {
			walkErr = err
			break
		}
	}
	close(jobs)
	wg.Wait()
	close(results)
	<-done
	st.Files, st.Bytes, st.Skipped = int(files), bytes, int(skipped)
	if walkErr != nil {
		return nil, st, walkErr
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		if all[i].Line != all[j].Line {
			return all[i].Line < all[j].Line
		}
		return all[i].Col < all[j].Col
	})
	st.Duration = time.Since(start)
	return all, st, nil
}

func excluded(rel, base string, pats []string) bool {
	for _, p := range pats {
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if strings.HasSuffix(p, "/**") && strings.HasPrefix(rel+"/", strings.TrimSuffix(p, "**")) {
			return true
		}
	}
	return false
}
