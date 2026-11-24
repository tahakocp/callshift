package scan

import (
	"regexp"
	"strings"

	"github.com/tahakocp/callshift/internal/change"
)

type specPath struct {
	op   change.Op
	segs []string
	// full holds segs behind each base path; baseLen the base lengths.
	full    [][]string
	baseLen []int
}

type matcher struct {
	paths []specPath
	bases [][]string
	hosts map[string]bool
	// methods available on each path template
	methods map[string][]string
	// literals holds every literal segment of every spec path. A match
	// needs at least one, so URLs without any are rejected at once.
	literals map[string]bool
}

func newMatcher(cs *change.Set) *matcher {
	m := &matcher{hosts: map[string]bool{}, methods: map[string][]string{}, literals: map[string]bool{}}
	for _, h := range cs.Hosts {
		m.hosts[strings.ToLower(h)] = true
	}
	for _, b := range cs.BasePaths {
		m.bases = append(m.bases, splitPath(b))
	}
	seen := map[string]bool{}
	for _, o := range cs.Ops {
		m.methods[o.Path] = append(m.methods[o.Path], o.Method)
		if seen[o.Path] {
			continue
		}
		seen[o.Path] = true
		p := o.Path
		if q := strings.IndexByte(p, '?'); q >= 0 {
			// Some specs key a variant of a path as "/x?beta=true"; the
			// plain path stands for both.
			p = p[:q]
			if seen[p] {
				continue
			}
		}
		sp := specPath{op: o, segs: splitPath(p)}
		for _, seg := range sp.segs {
			if !isParam(seg) {
				m.literals[seg] = true
			}
		}
		m.paths = append(m.paths, sp)
	}
	for i := range m.paths {
		sp := &m.paths[i]
		for _, b := range m.bases {
			sp.full = append(sp.full, append(append([]string{}, b...), sp.segs...))
			sp.baseLen = append(sp.baseLen, len(b))
		}
	}
	return m
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isParam(s string) bool { return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") }

// pathMatch is one spec path a URL in code can refer to.
type pathMatch struct {
	path     string
	score    int
	litHits  int // literal segments equal to literal spec segments, outside the base path
	withBase bool
}

// match returns the best matching spec paths for a URL found in code.
func (m *matcher) match(u *urlRef) []pathMatch {
	if u.hasHost && !strings.Contains(u.host, "\x00") && len(m.hosts) > 0 && !m.hosts[u.host] {
		return nil
	}
	anyLit := false
	for _, c := range u.segs {
		if !c.hole && !c.tmpl && !c.partial && m.literals[c.text] {
			anyLit = true
			break
		}
	}
	if !anyLit {
		return nil
	}
	n := len(u.segs)
	var best []pathMatch
	for _, sp := range m.paths {
		variants, baseLen := sp.full, sp.baseLen
		if !u.hasHost || len(m.bases) == 0 {
			variants = append(variants[:len(variants):len(variants)], sp.segs)
			baseLen = append(baseLen[:len(baseLen):len(baseLen)], 0)
		}
		for vi, full := range variants {
			bl := baseLen[vi]
			// A leading hole or a relative path may stand for any prefix
			// of the base path.
			maxSkip := 0
			if u.leadHole || u.relative {
				maxSkip = bl
			}
			for sk := 0; sk <= maxSkip; sk++ {
				// Each code segment covers one or two spec segments.
				if rest := len(full) - sk; rest < n || rest > 2*n {
					continue
				}
				sc, lit, ok := matchSegs(u.segs, full[sk:], bl-sk)
				if !ok {
					continue
				}
				pm := pathMatch{path: sp.op.Path, score: sc, litHits: lit, withBase: bl-sk > 0}
				switch {
				case len(best) == 0 || pm.score > best[0].score:
					best = []pathMatch{pm}
				case pm.score == best[0].score:
					dup := false
					for _, b := range best {
						if b.path == pm.path {
							dup = true
						}
					}
					if !dup {
						best = append(best, pm)
					}
				}
			}
		}
	}
	if len(best) > 0 && best[0].litHits == 0 {
		return nil
	}
	return best
}

// matchSegs matches code segments against spec segments. The first
// baseLen spec segments belong to the server base path. A whole-hole code
// segment may cover one or two spec segments (a variable holding
// "owner/repo" is common).
func matchSegs(code []seg, spec []string, baseLen int) (score, lit int, ok bool) {
	var rec func(i, j int) (int, int, bool)
	rec = func(i, j int) (int, int, bool) {
		if i == len(code) {
			if j == len(spec) {
				return 0, 0, true
			}
			return 0, 0, false
		}
		if j == len(spec) {
			return 0, 0, false
		}
		c := code[i]
		s := spec[j]
		inBase := j < baseLen
		switch {
		case c.hole:
			bestS, bestL, found := -1, 0, false
			for span := 1; span <= 2 && j+span <= len(spec); span++ {
				if span == 2 && (inBase || i == 0) {
					break
				}
				if sc, l, ok := rec(i+1, j+span); ok && sc > bestS {
					bestS, bestL, found = sc, l, true
				}
			}
			return bestS, bestL, found
		case c.tmpl:
			if !isParam(s) {
				return 0, 0, false
			}
			sc, l, ok := rec(i+1, j+1)
			return sc + 2, l, ok
		case c.partial:
			if isParam(s) {
				sc, l, ok := rec(i+1, j+1)
				return sc + 1, l, ok
			}
			if !partialMatch(c.text, s) {
				return 0, 0, false
			}
			sc, l, ok := rec(i+1, j+1)
			return sc + 2, l, ok
		default:
			if isParam(s) {
				if partialParam(s) && !partialMatch(s, c.text) {
					return 0, 0, false
				}
				sc, l, ok := rec(i+1, j+1)
				return sc + 1, l, ok
			}
			if c.text != s {
				return 0, 0, false
			}
			sc, l, ok := rec(i+1, j+1)
			if inBase {
				return sc + 2, l, ok
			}
			return sc + 4, l + 1, ok
		}
	}
	return rec(0, 0)
}

// partialParam reports a spec segment that mixes text and a parameter,
// such as "{id}.json" or "v{version}".
func partialParam(s string) bool {
	return strings.Count(s, "{") == 1 && (!strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}"))
}

var braceRe = regexp.MustCompile(`\{[^}]*\}`)

// partialMatch matches a code segment with holes ("file-\x00" or
// "file-{id}") against a spec literal.
func partialMatch(code, spec string) bool {
	code = braceRe.ReplaceAllString(code, "\x00")
	if !strings.Contains(code, "\x00") {
		return code == spec
	}
	var b strings.Builder
	b.WriteString("^")
	for i, piece := range strings.Split(code, "\x00") {
		if i > 0 {
			b.WriteString(".+")
		}
		b.WriteString(regexp.QuoteMeta(piece))
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(spec)
}
