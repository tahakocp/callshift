package scan

import (
	"regexp"
	"strings"
)

// seg is one path segment of a URL found in code.
type seg struct {
	text    string // literal text with \x00 for holes
	off     int    // file offset of the literal text, -1 if unknown
	hole    bool   // the whole segment is an interpolation
	tmpl    bool   // {name}, :name or a printf verb: a placeholder
	partial bool   // literal with a hole inside, e.g. "file-${id}"
}

type urlRef struct {
	method   string // from "GET /path" route strings
	host     string
	hasHost  bool
	leadHole bool // starts with an interpolation (a base URL variable)
	relative bool // no leading slash
	segs     []seg
	query    []key
}

// flatExpr is a string expression flattened to bytes, with \x00 for holes
// and the file offset of every byte (-1 for holes and escaped text).
type flatExpr struct {
	s    []byte
	offs []int
}

func flatten(ts []*token) flatExpr {
	var f flatExpr
	for _, t := range ts {
		if t == nil {
			f.s = append(f.s, 0)
			f.offs = append(f.offs, -1)
			continue
		}
		for _, p := range t.parts {
			if p.hole {
				f.s = append(f.s, 0)
				f.offs = append(f.offs, -1)
				continue
			}
			for i := 0; i < len(p.text); i++ {
				f.s = append(f.s, p.text[i])
				if p.off >= 0 {
					f.offs = append(f.offs, p.off+i)
				} else {
					f.offs = append(f.offs, -1)
				}
			}
		}
	}
	return f
}

var (
	routeRe   = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+`)
	printfRe  = regexp.MustCompile(`%[-+#0 ]*[0-9]*[sdvqxXfi]`)
	segNameRe = regexp.MustCompile(`^[A-Za-z0-9_.~\-\x00{}:@$%+,;=!*']+$`)
)

// parseURL reads a flattened string expression as an HTTP path or URL.
// It returns nil when the text does not look like one.
func parseURL(f flatExpr) *urlRef {
	b := append([]byte(nil), f.s...)
	offs := append([]int(nil), f.offs...)
	// printf verbs become holes.
	for _, m := range printfRe.FindAllIndex(b, -1) {
		for k := m[0]; k < m[1]; k++ {
			b[k] = 0
			offs[k] = -1
		}
	}
	s := string(b)
	trim := 0
	for trim < len(s) && (s[trim] == ' ' || s[trim] == '\t' || s[trim] == '\n') {
		trim++
	}
	s, offs = s[trim:], offs[trim:]
	u := &urlRef{}
	if m := routeRe.FindStringSubmatch(s); m != nil {
		u.method = m[1]
		s, offs = s[len(m[0]):], offs[len(m[0]):]
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://"):
		k := strings.Index(s, "://") + 3
		e := k
		for e < len(s) && s[e] != '/' && s[e] != '?' && s[e] != '#' && s[e] != ' ' && s[e] != '"' {
			e++
		}
		u.hasHost = true
		u.host = strings.ToLower(s[k:e])
		if i := strings.LastIndexByte(u.host, ':'); i > 0 && !strings.Contains(u.host[i:], "\x00") {
			u.host = u.host[:i]
		}
		s, offs = s[e:], offs[e:]
		if s == "" {
			return nil
		}
	case strings.HasPrefix(s, "\x00"):
		u.leadHole = true
		for len(s) > 0 && s[0] == 0 {
			s, offs = s[1:], offs[1:]
		}
	}
	if !strings.HasPrefix(s, "/") {
		if u.hasHost || s == "" {
			return nil
		}
		if !u.leadHole {
			u.relative = true
		}
		// "v1/chat/completions" style: needs a slash and no spaces.
		if !strings.Contains(s, "/") {
			return nil
		}
	}
	// path ends at whitespace or quote
	end := len(s)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '"' || c == '\'' || c == '`' || c == '#' || c == '\\' {
			end = i
			break
		}
	}
	if end < len(s) && !(u.method != "" || u.hasHost) {
		// Text after the path in a plain literal ("/v1/x is deprecated") means prose.
		rest := strings.TrimSpace(s[end:])
		if rest != "" && !strings.HasPrefix(rest, "\x00") && s[end] == ' ' {
			return nil
		}
	}
	s, offs = s[:end], offs[:end]
	if q := strings.IndexByte(s, '?'); q >= 0 {
		qs := s[q+1:]
		qo := offs[q+1:]
		pos := 0
		for _, kv := range strings.Split(qs, "&") {
			name := kv
			if e := strings.IndexByte(kv, '='); e >= 0 {
				name = kv[:e]
			}
			if name != "" && !strings.Contains(name, "\x00") {
				u.query = append(u.query, key{name: name, off: qo[pos], exact: qo[pos] >= 0})
			}
			pos += len(kv) + 1
		}
		s, offs = s[:q], offs[:q]
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' {
			if i > start {
				t := s[start:i]
				if !segNameRe.MatchString(t) {
					return nil
				}
				sg := seg{text: t, off: offs[start]}
				switch {
				case strings.Trim(t, "\x00") == "":
					sg.hole = true
				case (strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") && !strings.Contains(t[1:len(t)-1], "{")) || (strings.HasPrefix(t, ":") && len(t) > 1) || (strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">")):
					sg.tmpl = true
				case strings.Contains(t, "\x00") || strings.Contains(t, "{"):
					sg.partial = true
				}
				for k := start; k < i; k++ {
					if offs[k] < 0 && s[k] != 0 {
						sg.off = -1
					}
				}
				u.segs = append(u.segs, sg)
			}
			start = i + 1
		}
	}
	if len(u.segs) == 0 {
		return nil
	}
	return u
}

// litSegs counts plain literal segments.
func (u *urlRef) litSegs() int {
	n := 0
	for _, s := range u.segs {
		if !s.hole && !s.tmpl && !s.partial {
			n++
		}
	}
	return n
}
