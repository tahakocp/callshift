package change

import (
	"regexp"
	"strings"

	"github.com/tahakocp/callshift/internal/oas"
)

// Replacement phrases that API docs use when they deprecate something.
// The name has to be in backticks or a markdown link so that ordinary prose
// ("use this field instead of ...") does not produce false hints.
var hintRe = regexp.MustCompile("(?i)(?:in favou?r of|use|replaced (?:by|with)|superseded by|migrate to|switch to|instead,? use|prefer)\\s+(?:the\\s+)?(?:new\\s+)?\\[?`([A-Za-z0-9_.\\-/{} ]+?)`")

func hint(desc string) string {
	m := hintRe.FindStringSubmatch(desc)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// replacementField returns the sibling field that the description of f
// names as its replacement, if that sibling exists.
func replacementField(f oas.Field, all []oas.Field) string {
	h := hint(f.Description)
	if h == "" {
		return ""
	}
	parent := parentOf(f.Path)
	for _, g := range all {
		if g.Path == f.Path {
			continue
		}
		if g.Name == h && parentOf(g.Path) == parent {
			return g.Path
		}
	}
	return ""
}

func replacementParam(desc string, all []oas.Param, p oas.Param) string {
	h := hint(desc)
	if h == "" {
		return ""
	}
	for _, q := range all {
		if q.Name == h && q.In == p.In && q.Name != p.Name {
			return q.Name
		}
	}
	return ""
}

// pairField looks for an added field that most likely replaces a removed one.
// Evidence, strongest first: the old description named it as a replacement;
// same parent, same type and similar name; the only removed/added pair of
// the same type under one parent.
func pairField(r oas.Field, added []oas.Field, used map[string]bool) (*oas.Field, string) {
	parent := parentOf(r.Path)
	var sameParent []oas.Field
	for _, a := range added {
		if !used[a.Path] && parentOf(a.Path) == parent {
			sameParent = append(sameParent, a)
		}
	}
	if h := hint(r.Description); h != "" {
		for i, a := range sameParent {
			if a.Name == h {
				return &sameParent[i], "old description names `" + h + "`"
			}
		}
	}
	best := -1
	bestScore := 0.0
	for i, a := range sameParent {
		if r.Type == "" || a.Type != r.Type {
			continue
		}
		sc := nameSimilarity(r.Name, a.Name)
		if sc > bestScore {
			best, bestScore = i, sc
		}
	}
	if best >= 0 && bestScore >= 0.5 {
		return &sameParent[best], "same type, similar name"
	}
	return nil, ""
}

func pairParam(r oas.Param, added []oas.Param, used map[string]bool) (*oas.Param, string) {
	if h := hint(r.Description); h != "" {
		for i, a := range added {
			if a.Name == h && a.In == r.In && !used[a.In+":"+a.Name] {
				return &added[i], "old description names `" + h + "`"
			}
		}
	}
	best := -1
	bestScore := 0.0
	for i, a := range added {
		if used[a.In+":"+a.Name] || a.In != r.In || a.Type != r.Type {
			continue
		}
		sc := nameSimilarity(r.Name, a.Name)
		if sc > bestScore {
			best, bestScore = i, sc
		}
	}
	if best >= 0 && bestScore >= 0.5 {
		return &added[best], "same type, similar name"
	}
	return nil, ""
}

// nameSimilarity compares names as word sets (snake, kebab and camel case),
// so max_tokens ~ max_output_tokens scores 2/3.
func nameSimilarity(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range wa {
		set[w] = true
	}
	inter := 0
	for _, w := range wb {
		if set[w] {
			inter++
			delete(set, w)
		}
	}
	// Overlap coefficient over the shorter name, scaled down when the
	// lengths differ a lot.
	short := len(wa)
	if len(wb) < short {
		short = len(wb)
	}
	long := len(wa) + len(wb) - short
	return float64(inter) / float64(short) * (0.5 + 0.5*float64(short)/float64(long))
}

func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	prevLower := false
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ':
			flush()
			prevLower = false
		case r >= 'A' && r <= 'Z':
			if prevLower {
				flush()
			}
			cur = append(cur, r)
			prevLower = false
		default:
			cur = append(cur, r)
			prevLower = r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		}
	}
	flush()
	return out
}
