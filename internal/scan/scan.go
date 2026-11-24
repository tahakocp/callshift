package scan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/tahakocp/callshift/internal/change"
)

// Confidence that a finding is real.
const (
	Low    = 1
	Medium = 2
	High   = 3
)

// ConfidenceName turns a level into text.
func ConfidenceName(c int) string {
	switch c {
	case High:
		return "high"
	case Medium:
		return "medium"
	}
	return "low"
}

// ParseConfidence reads "low", "medium" or "high".
func ParseConfidence(s string) (int, bool) {
	switch strings.ToLower(s) {
	case "low":
		return Low, true
	case "medium", "med":
		return Medium, true
	case "high":
		return High, true
	}
	return 0, false
}

// Edit replaces file bytes [Off, End) with New.
type Edit struct {
	Off int    `json:"offset"`
	End int    `json:"end"`
	New string `json:"new"`
	Old string `json:"old"`
}

// Finding is one place in code affected by one change.
type Finding struct {
	File          string        `json:"file"`
	Line          int           `json:"line"`
	Col           int           `json:"column"`
	Kind          string        `json:"kind"`
	Severity      string        `json:"severity"`
	Confidence    string        `json:"confidence"`
	Level         int           `json:"-"`
	Method        string        `json:"method"`
	Path          string        `json:"path"`
	MethodGuessed bool          `json:"method_guessed,omitempty"`
	Name          string        `json:"name,omitempty"`
	Message       string        `json:"message"`
	Snippet       string        `json:"snippet,omitempty"`
	Fix           *Edit         `json:"fix,omitempty"`
	Change        change.Change `json:"change"`
}

// key is a name used inside a call: an object key, a query parameter, a
// keyword argument, a JSON key inside a string, a Go struct tag.
type key struct {
	name  string
	off   int
	exact bool // off points at exactly name in the file
	value *token
	line  int
	weak  bool // found outside the call, in the surrounding function
	// config: a sibling of the URL in a settings object, which may or may
	// not be sent with the request
	config bool
}

// call is one recognised use of an operation.
type call struct {
	url      *urlRef
	urlTok   *token
	first    int // token index range of the string expression
	last     int
	span     [2]int // token range that holds arguments
	method   string
	guessed  bool
	paths    []pathMatch
	conf     int
	keys     []key
	strs     []*token
	bodySeen bool
	config   bool // the URL sits in a settings object, not in a call
}

var methodNames = map[string]string{"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE", "del": "DELETE", "head": "HEAD", "options": "OPTIONS"}

// File scans one source file.
func File(name, src string, cs *change.Set, m *matcher, idx *changeIndex) []Finding {
	l := langFor(name)
	if l == nil {
		return nil
	}
	toks := lex(src, l)
	fileHasHost := false
	for h := range m.hosts {
		if strings.Contains(src, h) {
			fileHasHost = true
			break
		}
	}
	var goTags []key
	if l == langGo {
		goTags = goJSONTags(src)
	}
	lines := lineStarts(src)
	fns := functionBodies(toks, l)
	var out []Finding
	used := map[int]bool{}
	for i := 0; i < len(toks); i++ {
		if toks[i].kind != tStr || used[i] {
			continue
		}
		first, last, parts := stringExpr(toks, i, l)
		for k := first; k <= last; k++ {
			used[k] = true
		}
		u := parseURL(flatten(parts))
		if u == nil {
			continue
		}
		pms := m.match(u)
		if len(pms) == 0 {
			continue
		}
		spans := spansFor(toks, first, last, l)
		for _, sp := range spans {
			c := &call{url: u, urlTok: &toks[i], first: first, last: last, paths: pms}
			c.conf = confidence(u, pms[0], fileHasHost)
			c.span = sp
			c.method, c.guessed = inferMethod(toks, c, l)
			c.keys, c.strs, c.bodySeen = collectKeys(toks, c.span, first, last, l, fns)
			// $ch = curl_init(URL); curl_setopt($ch, ..., json_encode($data)):
			// later calls that use the handle belong to the same request.
			for _, h := range handleUses(toks, sp) {
				ks, strs, seen := collectKeys(toks, h, first, last, l, fns)
				c.keys = append(c.keys, ks...)
				c.strs = append(c.strs, strs...)
				c.bodySeen = c.bodySeen || seen
			}
			if configObject(toks, sp) {
				// The URL is a value in a settings object, not an argument
				// of a call: its siblings are not proven to be sent.
				c.config = true
				for k := range c.keys {
					c.keys[k].weak = true
					c.keys[k].config = true
				}
			}
			if !c.bodySeen {
				// The body may be built in other statements of the same
				// function (Java, PHP curl, Go). Look there, with less trust.
				if b := enclosingBlock(toks, sp, l); b[1] > b[0] {
					ks, strs, seen := collectKeys(toks, b, first, last, l, fns)
					for k := range ks {
						ks[k].weak = true
					}
					c.keys, c.strs, c.bodySeen = ks, append(c.strs, strs...), seen
					if c.method == "" {
						c.span = b
						c.method, c.guessed = inferMethod(toks, c, l)
						c.span = sp
					}
				}
			}
			c.keys = append(c.keys, u.query...)
			out = append(out, check(name, src, lines, toks, c, m, idx, goTags)...)
		}
		i = last
	}
	return keepStrongest(out)
}

// keepStrongest drops repeats of the same finding reached through two
// calls (a URL variable used twice), keeping the higher confidence.
func keepStrongest(fs []Finding) []Finding {
	best := map[string]int{}
	var out []Finding
	for _, f := range fs {
		k := fmt.Sprintf("%d:%d:%s:%s", f.Line, f.Col, f.Kind, f.Name)
		if i, ok := best[k]; ok {
			if f.Level > out[i].Level {
				out[i] = f
			}
			continue
		}
		best[k] = len(out)
		out = append(out, f)
	}
	return out
}

// stringExpr extends a string token across concatenation ("a" + x + "b")
// and returns the token range plus the string tokens, nil for holes.
func stringExpr(toks []token, i int, l *lang) (int, int, []*token) {
	concat := "+"
	if l == langPHP {
		concat = "."
	}
	parts := []*token{&toks[i]}
	first, last := i, i
	// leading "base + " or "base." -> hole
	if i >= 2 && toks[i-1].is(concat) && (toks[i-2].kind == tIdent || toks[i-2].is(")") || toks[i-2].is("]")) {
		parts = append([]*token{nil}, parts...)
	}
	j := i + 1
	for j+1 < len(toks) && toks[j].is(concat) {
		n := &toks[j+1]
		if n.kind == tStr {
			parts = append(parts, n)
			last = j + 1
			j += 2
			continue
		}
		// operand: ident(.ident|(...)|[...])*
		k := j + 1
		if toks[k].kind != tIdent && toks[k].kind != tNum && !toks[k].is("(") {
			break
		}
		k = skipOperand(toks, k)
		parts = append(parts, nil)
		last = k - 1
		j = k
	}
	// Python "%s/v1/x" % base and "...".format() are handled by printf/{} holes.
	return first, last, parts
}

func skipOperand(toks []token, k int) int {
	for k < len(toks) {
		t := &toks[k]
		switch {
		case t.kind == tIdent || t.kind == tNum:
			k++
		case t.is(".") || t.is("::") || t.is("->") || t.is("?."):
			k++
		case t.is("(") || t.is("["):
			k = matchClose(toks, k) + 1
		default:
			return k
		}
		if k < len(toks) && !(toks[k].is(".") || toks[k].is("(") || toks[k].is("[") || toks[k].is("::") || toks[k].is("->")) {
			return k
		}
	}
	return k
}

var openers = map[string]string{"(": ")", "[": "]", "{": "}", "${{": "}}"}

func matchClose(toks []token, i int) int {
	open := toks[i].text
	cl := openers[open]
	depth := 0
	for k := i; k < len(toks); k++ {
		if toks[k].kind != tPunct {
			continue
		}
		switch toks[k].text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
			if depth == 0 {
				if toks[k].text != cl {
					return k
				}
				return k
			}
		}
	}
	return len(toks) - 1
}

// spansFor returns one span per call that uses the string: the call it
// sits in, or every call that uses the variable it is assigned to.
func spansFor(toks []token, first, last int, l *lang) [][2]int {
	if l.bareWords {
		return [][2]int{logicalLine(toks, first, last)}
	}
	sp := spanFor(toks, first, last, l)
	if sp[0] == first && sp[1] == last {
		if uses := variableUses(toks, first, last); len(uses) > 0 {
			return uses
		}
	}
	return [][2]int{sp}
}

// spanFor finds the token range that holds the call's other arguments.
func spanFor(toks []token, first, last int, l *lang) [2]int {
	depth := 0
	startLine := toks[first].line
	for k := first - 1; k >= 0; k-- {
		t := &toks[k]
		if startLine-t.line > 40 {
			break
		}
		if t.kind != tPunct {
			continue
		}
		switch t.text {
		case ")", "]", "}":
			depth++
		case "(", "[", "{":
			if depth > 0 {
				depth--
				continue
			}
			if t.text == "{" && !objectBrace(toks, k) {
				return [2]int{first, last}
			}
			end := matchClose(toks, k)
			// An object literal inside a call: widen to the call.
			if t.text == "{" && k > 0 && (toks[k-1].is("(") || toks[k-1].is(",")) {
				if o := enclosingOpen(toks, k); o >= 0 && toks[o].is("(") {
					return [2]int{o, matchClose(toks, o)}
				}
			}
			return [2]int{k, end}
		case ";":
			if depth == 0 {
				return [2]int{first, last}
			}
		}
	}
	return [2]int{first, last}
}

func enclosingOpen(toks []token, i int) int {
	depth := 0
	for k := i - 1; k >= 0 && i-k < 400; k-- {
		t := &toks[k]
		if t.kind != tPunct {
			continue
		}
		switch t.text {
		case ")", "]", "}":
			depth++
		case "(", "[", "{":
			if depth == 0 {
				return k
			}
			depth--
		}
	}
	return -1
}

// objectBrace tells an object/dict literal from a code block.
func objectBrace(toks []token, k int) bool {
	if k == 0 {
		return false
	}
	p := &toks[k-1]
	if p.kind == tPunct {
		switch p.text {
		case "(", ",", ":", "=", "[", "=>", "?", "||", "{", ":=", "<<":
			return true
		}
		return false
	}
	if p.kind == tIdent {
		switch p.text {
		case "return", "yield", "await", "json", "data", "body":
			return true
		}
		// Go composite literal: Type{...} after = or :=
		if k >= 2 && (toks[k-2].is("=") || toks[k-2].is(":=") || toks[k-2].is("&") || toks[k-2].is(".")) {
			return true
		}
	}
	return false
}

// assignedName returns the variable, constant or field a string is
// assigned to: url = "...", const URL = "...", self.url = "...", URL: "...".
func assignedName(toks []token, first int) string {
	// Skip a fallback in front: url = process.env.X || "https://..."
	k0 := first - 1
	for k := first - 1; k >= 0 && k >= first-12; k-- {
		t := &toks[k]
		if t.is("||") || t.is("??") || (t.kind == tIdent && t.text == "or") || (t.is("|") && k > 0 && toks[k-1].is("|")) || (t.is("?") && k > 0 && toks[k-1].is("?")) {
			k0 = k - 1
			for k0 >= 0 && (toks[k0].kind == tIdent || toks[k0].is(".") || toks[k0].is("|") || toks[k0].is("?")) {
				k0--
			}
			break
		}
		if t.is("=") || t.is(":=") || t.is(";") || t.is("{") || t.is("(") {
			break
		}
	}
	for k := k0; k >= 0 && k >= k0-4; k-- {
		t := &toks[k]
		if t.is("=") || t.is(":=") || (t.is(":") && k > 0 && toks[k-1].kind == tIdent) {
			if k > 0 && toks[k-1].kind == tIdent {
				return toks[k-1].text
			}
			// Go "const X string = ..." / TS "const X: string = ..."
			if k > 1 && toks[k-1].kind == tIdent && toks[k-2].kind == tIdent {
				return toks[k-2].text
			}
			return ""
		}
		if t.kind == tIdent && k > 0 && toks[k-1].kind == tIdent && t.text != "" {
			continue // type name in "X string ="
		}
	}
	return ""
}

// variableUses finds every later call that receives the variable holding
// the string, anywhere in the file.
func variableUses(toks []token, first, last int) [][2]int {
	name := assignedName(toks, first)
	if name == "" || len(name) < 2 {
		return nil
	}
	var out [][2]int
	seen := map[int]bool{}
	// Uses after the assignment, until the name is assigned again (another
	// function's "const url = ..." starts a new variable).
	for k := last + 1; k < len(toks); k++ {
		if toks[k].kind != tIdent || toks[k].text != name {
			continue
		}
		if k+1 < len(toks) && (toks[k+1].is("=") || toks[k+1].is(":=")) && !(k+2 < len(toks) && toks[k+2].is("=")) {
			break
		}
		o := enclosingOpen(toks, k)
		if o >= 0 && toks[o].is("{") && objectBrace(toks, o) {
			// $.ajax({ url: apiUrl, ... }) or a request config object
			if p := enclosingOpen(toks, o); p >= 0 && toks[p].is("(") {
				o = p
			}
		}
		if o < 0 || !(toks[o].is("(") || toks[o].is("{") && objectBrace(toks, o)) || seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, [2]int{o, matchClose(toks, o)})
	}
	return out
}

// enclosingBlock is the function body around a span: the nearest code
// block in brace languages, or the def ... def region in Python and Ruby.
// It is capped at 150 lines.
func enclosingBlock(toks []token, sp [2]int, l *lang) [2]int {
	if l == langPy || l == langRuby {
		a, b := sp[0], sp[1]
		for a > 0 && !(toks[a].kind == tIdent && toks[a].text == "def") && toks[sp[0]].line-toks[a].line < 150 {
			a--
		}
		for b+1 < len(toks) && !(toks[b+1].kind == tIdent && (toks[b+1].text == "def" || toks[b+1].text == "class")) && toks[b].line-toks[sp[1]].line < 150 {
			b++
		}
		return [2]int{a, b}
	}
	k := sp[0]
	for {
		o := enclosingOpen(toks, k)
		if o < 0 {
			return [2]int{0, 0}
		}
		if toks[o].is("{") && !objectBrace(toks, o) {
			e := matchClose(toks, o)
			if toks[e].line-toks[o].line > 150 {
				return [2]int{0, 0}
			}
			return [2]int{o, e}
		}
		k = o
	}
}

// logicalLine is the shell command around a token, following "\" line
// continuations.
func logicalLine(toks []token, first, last int) [2]int {
	a := first
	for a > 0 {
		p := &toks[a-1]
		if p.line == toks[a].line || (p.is("\\") && p.line == toks[a].line-1) || (toks[a-1].line == toks[a].line-1 && a >= 2 && toks[a-2].is("\\")) {
			a--
			continue
		}
		break
	}
	b := last
	for b+1 < len(toks) {
		n := &toks[b+1]
		if n.line == toks[b].line || (toks[b].is("\\") && n.line == toks[b].line+1) {
			b++
			continue
		}
		break
	}
	return [2]int{a, b}
}

func inferMethod(toks []token, c *call, l *lang) (string, bool) {
	if c.url.method != "" {
		return c.url.method, false
	}
	// Function name right before the call: axios.post(, requests.get(,
	// http.Get(, client.Delete(.
	open := c.span[0]
	if toks[open].is("(") && open > 0 && toks[open-1].kind == tIdent {
		if m, ok := methodNames[strings.ToLower(toks[open-1].text)]; ok {
			return m, false
		}
		if m := methodSuffix(toks[open-1].text); m != "" {
			return m, false
		}
	}
	for k := c.first - 1; k >= 0 && k >= c.first-3; k-- {
		if toks[k].kind == tIdent {
			if m, ok := methodNames[strings.ToLower(toks[k].text)]; ok && k+1 < len(toks) && toks[k+1].is("(") {
				return m, false
			}
		}
	}
	shellData := false
	for k := c.span[0]; k <= c.span[1] && k < len(toks); k++ {
		t := &toks[k]
		switch t.kind {
		case tIdent:
			lt := strings.ToLower(t.text)
			if (lt == "method" || lt == "http_method" || lt == "verb") && k+2 < len(toks) && (toks[k+1].is(":") || toks[k+1].is("=") || toks[k+1].is("=>")) {
				v := &toks[k+2]
				if v.kind == tStr {
					if m, ok := methodNames[strings.ToLower(v.literal())]; ok {
						return m, false
					}
				}
				if v.kind == tIdent {
					if m := methodSuffix(v.text); m != "" {
						return m, false
					}
					if k+4 < len(toks) && toks[k+3].is(".") {
						if m, ok := methodNames[strings.ToLower(toks[k+4].text)]; ok {
							return m, false
						}
					}
				}
			}
			if l.bareWords && (t.text == "X" || t.text == "request") && k > 0 && toks[k-1].is("-") && k+1 < len(toks) {
				if m, ok := methodNames[strings.ToLower(toks[k+1].text)]; ok {
					return m, false
				}
			}
			if l.bareWords && (t.text == "d" || t.text == "data" || t.text == "F" || t.text == "form" || t.text == "json" || t.text == "data-raw" || t.text == "data-binary") && k > 0 && toks[k-1].is("-") {
				shellData = true
			}
			if m := methodSuffix(t.text); m != "" && k > c.last {
				return m, false
			}
		case tStr:
			if k < c.first || k > c.last {
				if m, ok := methodNames[strings.ToLower(t.literal())]; ok && strings.ToUpper(t.literal()) == t.literal() {
					return m, false
				}
			}
		}
	}
	if shellData {
		return "POST", true
	}
	return "", true
}

// methodSuffix reads names like MethodPost, HttpMethod.Post, HTTP_POST.
func methodSuffix(s string) string {
	ls := strings.ToLower(s)
	for _, p := range []string{"method", "http_", "httpmethod"} {
		if strings.HasPrefix(ls, p) {
			if m, ok := methodNames[strings.TrimPrefix(ls, p)]; ok {
				return m
			}
		}
	}
	return ""
}

var selfName = map[string]bool{"self": true, "this": true, "cls": true, "@": true, "$this": true}

var putNames = map[string]bool{"put": true, "addProperty": true, "putOpt": true, "set": true, "with": true, "append": true, "add": true, "Set": true, "Add": true, "putString": true, "putInt": true}

var jsonKeyRe = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_\-]*)"\s*:`)

// collectKeys gathers names used in the call span, plus the bodies of
// variables passed into it that were built a few lines earlier.
func collectKeys(toks []token, span [2]int, first, last int, l *lang, fns map[string][2]int) ([]key, []*token, bool) {
	var keys []key
	var strs []*token
	bodySeen := false
	visit := func(a, b int, depth int) {}
	var walk func(a, b, depth int)
	seenVar := map[string]bool{}
	walk = func(a, b, depth int) {
		for k := a; k <= b && k < len(toks); k++ {
			if k >= first && k <= last {
				continue
			}
			t := &toks[k]
			next := func(n int) *token {
				if k+n < len(toks) {
					return &toks[k+n]
				}
				return nil
			}
			switch t.kind {
			case tIdent:
				// body.max_tokens = 5
				if n := next(1); n != nil && n.is("=") && k > 1 && toks[k-1].is(".") && !(next(2) != nil && next(2).is("=")) && !selfName[toks[k-2].text] {
					keys = append(keys, key{name: t.text, off: t.off, exact: true, value: valueAfter(toks, k+2), line: t.line})
					bodySeen = true
				}
				if n := next(1); n != nil && (n.is(":") || n.is("=>")) && !(k > 0 && toks[k-1].is("?")) {
					keys = append(keys, key{name: t.text, off: t.off, exact: true, value: valueAfter(toks, k+2), line: t.line})
					bodySeen = true
				}
				if n := next(1); n != nil && n.is("=") && insideDict(toks, k) {
					keys = append(keys, key{name: t.text, off: t.off, exact: true, value: valueAfter(toks, k+2), line: t.line})
					bodySeen = true
				}
				// { model, max_tokens } shorthand properties
				if k > 0 && (toks[k-1].is("{") || toks[k-1].is(",")) && next(1) != nil && (next(1).is(",") || next(1).is("}")) && l == langJS {
					if o := enclosingOpen(toks, k); o >= 0 && toks[o].is("{") && objectBrace(toks, o) {
						keys = append(keys, key{name: t.text, off: t.off, exact: false, line: t.line})
						bodySeen = true
					}
				}
				if depth < 4 && !seenVar[t.text] && !(next(1) != nil && (next(1).is("(") || next(1).is(":"))) {
					seenVar[t.text] = true
					for _, r := range assignedGroups(toks, k, t.text) {
						walk(r[0], r[1], depth+1)
					}
				}
				// Go: OpenAIRequest{...} or json.Marshal(OpenAIRequest{...}) -> that struct's tags
				if l == langGo && depth < 4 && next(1) != nil && next(1).is("{") && fns != nil && !seenVar["type "+t.text] {
					seenVar["type "+t.text] = true
					if r, ok := fns["type "+t.text]; ok {
						walk(r[0], r[1], depth+1)
					}
				}
				// payload = build_payload(...): follow a helper defined in this file
				if depth < 4 && next(1) != nil && next(1).is("(") && fns != nil && !seenVar["()"+t.text] {
					seenVar["()"+t.text] = true
					if r, ok := fns[t.text]; ok && !(r[0] <= span[0] && span[1] <= r[1]) {
						walk(r[0], r[1], depth+1)
					}
				}
			case tStr:
				strs = append(strs, t)
				if l == langGo && len(t.parts) == 1 && strings.Contains(t.parts[0].text, "json:") {
					p := t.parts[0]
					for _, mm := range goTagRe.FindAllStringSubmatchIndex(p.text, -1) {
						keys = append(keys, key{name: p.text[mm[2]:mm[3]], off: p.off + mm[2], exact: p.off >= 0, line: t.line})
					}
				}
				single := len(t.parts) == 1 && !t.parts[0].hole
				// obj.put("max_tokens", n), mapOf("max_tokens" to n), body["max_tokens"] = n
				putArg := k >= 2 && toks[k-1].is("(") && toks[k-2].kind == tIdent && putNames[toks[k-2].text] && next(1) != nil && next(1).is(",")
				kotlinTo := next(1) != nil && next(1).kind == tIdent && next(1).text == "to"
				indexSet := k >= 1 && toks[k-1].is("[") && next(1) != nil && next(1).is("]") && next(2) != nil && next(2).is("=") && !(next(3) != nil && next(3).is("="))
				if single && (putArg || kotlinTo || indexSet) {
					v := 2
					if indexSet {
						v = 3
					}
					keys = append(keys, key{name: t.parts[0].text, off: t.parts[0].off, exact: t.parts[0].off >= 0, value: valueAfter(toks, k+v), line: t.line})
					bodySeen = true
				}
				if n := next(1); n != nil && (n.is(":") || n.is("=>")) && single {
					keys = append(keys, key{name: t.parts[0].text, off: t.parts[0].off, exact: t.parts[0].off >= 0, value: valueAfter(toks, k+2), line: t.line})
					bodySeen = true
				}
				for _, p := range t.parts {
					if p.hole {
						continue
					}
					for _, mm := range jsonKeyRe.FindAllStringSubmatchIndex(p.text, -1) {
						off := -1
						if p.off >= 0 {
							off = p.off + mm[2]
						}
						keys = append(keys, key{name: p.text[mm[2]:mm[3]], off: off, exact: off >= 0, value: jsonValue(p, mm[1]), line: t.line})
						bodySeen = true
					}
				}
			}
		}
	}
	_ = visit
	walk(span[0], span[1], 0)
	return keys, strs, bodySeen
}

// jsonValue makes a pseudo token for a "string" value right after a JSON key
// inside a larger string literal.
func jsonValue(p part, after int) *token {
	rest := p.text[after:]
	trim := len(rest) - len(strings.TrimLeft(rest, " \t\n"))
	rest = rest[trim:]
	if !strings.HasPrefix(rest, `"`) {
		return nil
	}
	e := strings.IndexByte(rest[1:], '"')
	if e < 0 {
		return nil
	}
	off := -1
	if p.off >= 0 {
		off = p.off + after + trim + 1
	}
	v := rest[1 : 1+e]
	return &token{kind: tStr, text: `"` + v + `"`, parts: []part{{text: v, off: off}}, off: off - 1}
}

func valueAfter(toks []token, k int) *token {
	if k < len(toks) && toks[k].kind == tStr {
		return &toks[k]
	}
	return nil
}

// insideDict is true for Python dict(a=1) and Ruby/PHP-like keyword args
// inside a nested call, not for kwargs of the HTTP call itself.
func insideDict(toks []token, k int) bool {
	o := enclosingOpen(toks, k)
	return o > 0 && toks[o].is("(") && toks[o-1].kind == tIdent && toks[o-1].text == "dict"
}

// assignedGroup finds "name = {...}" / "name := T{...}" / "name = dict(...)"
// / "name, err := json.Marshal(x)" within 80 lines before token k and
// returns the token range of each such group (up to four, for branches).
func assignedGroups(toks []token, k int, name string) [][2]int {
	var out [][2]int
	line := toks[k].line
	for j := k - 1; j >= 1 && line-toks[j].line < 80; j-- {
		if toks[j].kind == tIdent && (toks[j].text == "func" || toks[j].text == "def" || toks[j].text == "function") {
			break // left the current function
		}
		if toks[j].kind != tIdent || toks[j].text != name || j+2 >= len(toks) {
			continue
		}
		a := j + 1
		// name, err := ...
		for a+1 < len(toks) && toks[a].is(",") && toks[a+1].kind == tIdent && toks[a+1].line == toks[j].line {
			a += 2
		}
		// TS/Go type annotations: name: Record<string, unknown> = {...}
		if toks[a].is(":") {
			for a < len(toks) && a < j+16 && !toks[a].is("=") && toks[a].line == toks[j].line {
				a++
			}
		}
		if a >= len(toks) || !(toks[a].is("=") || toks[a].is(":=")) || (a+1 < len(toks) && toks[a+1].is("=")) {
			continue
		}
		g := a + 1
		for g < len(toks) && g < a+12 {
			t := &toks[g]
			if t.is("[") && g > 0 && (toks[g-1].kind == tIdent && toks[g-1].text == "map" || g+1 < len(toks) && toks[g+1].is("]")) {
				g = matchClose(toks, g) + 1 // Go map[K]V / []T
				continue
			}
			if t.is("{") || t.is("(") || t.is("[") {
				break
			}
			if t.line != toks[j].line {
				break
			}
			g++
		}
		if g < len(toks) && (toks[g].is("{") || toks[g].is("(") || toks[g].is("[")) {
			out = append(out, [2]int{g, matchClose(toks, g)})
			if len(out) == 4 {
				break
			}
		}
	}
	return out
}

var goTagRe = regexp.MustCompile("json:\"([A-Za-z0-9_\\-]+)")

// goJSONTags returns every json struct tag name in a Go file.
func goJSONTags(src string) []key {
	var out []key
	for _, m := range goTagRe.FindAllStringSubmatchIndex(src, -1) {
		out = append(out, key{name: src[m[2]:m[3]], off: m[2], exact: true, line: 1 + strings.Count(src[:m[2]], "\n")})
	}
	return out
}

// confidence of a call match before looking at any change.
func confidence(u *urlRef, pm pathMatch, fileHasHost bool) int {
	switch {
	case u.hasHost:
		return High
	case u.method != "" && pm.litHits >= 1:
		return High
	case pm.withBase && pm.litHits >= 1:
		return High
	case pm.litHits >= 2 && fileHasHost:
		return High
	case pm.litHits >= 2 || fileHasHost:
		return Medium
	}
	return Low
}

func lineStarts(src string) []int {
	out := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			out = append(out, i+1)
		}
	}
	return out
}

func position(lines []int, off int) (int, int) {
	i := sort.Search(len(lines), func(i int) bool { return lines[i] > off }) - 1
	if i < 0 {
		i = 0
	}
	return i + 1, off - lines[i] + 1
}

func lineText(src string, lines []int, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	a := lines[line-1]
	b := len(src)
	if line < len(lines) {
		b = lines[line] - 1
	}
	t := strings.TrimSpace(src[a:b])
	if len(t) > 160 {
		t = t[:157] + "..."
	}
	return t
}

// functionBodies maps function names defined in the file to their bodies,
// so a payload built by a helper in the same file can be followed.
func functionBodies(toks []token, l *lang) map[string][2]int {
	out := map[string][2]int{}
	for k := 0; k+1 < len(toks); k++ {
		t := &toks[k]
		if t.kind != tIdent {
			continue
		}
		switch t.text {
		case "def":
			if l != langPy && l != langRuby {
				continue
			}
			name := toks[k+1].text
			e := k + 2
			for e < len(toks) && !(toks[e].kind == tIdent && (toks[e].text == "def" || toks[e].text == "class") && toks[e].line > t.line) {
				e++
			}
			out[name] = [2]int{k, e - 1}
		case "type":
			// Go: type Name struct { ... }
			if l == langGo && k+3 < len(toks) && toks[k+1].kind == tIdent && toks[k+2].kind == tIdent && toks[k+2].text == "struct" && toks[k+3].is("{") {
				out["type "+toks[k+1].text] = [2]int{k + 3, matchClose(toks, k+3)}
			}
		case "func", "function", "fn":
			n := k + 1
			if n < len(toks) && toks[n].is("(") { // Go method receiver
				n = matchClose(toks, n) + 1
			}
			if n >= len(toks) || toks[n].kind != tIdent {
				continue
			}
			name := toks[n].text
			b := n + 1
			for b < len(toks) && b < n+60 && !toks[b].is("{") {
				if toks[b].is("(") {
					b = matchClose(toks, b)
				}
				b++
			}
			if b < len(toks) && toks[b].is("{") {
				out[name] = [2]int{b, matchClose(toks, b)}
			}
		}
	}
	return out
}

// configObject is true for a span that is an object or array literal which
// is not itself an argument of a call.
func configObject(toks []token, sp [2]int) bool {
	o := sp[0]
	if !(toks[o].is("{") || toks[o].is("[")) {
		return false
	}
	return !(o > 0 && (toks[o-1].is("(") || toks[o-1].is(",")))
}

// handleUses finds "h = f(URL)" or "h = new F(URL)" and returns the
// argument groups of later calls, within 40 lines, that take h as an
// argument or are called on it.
func handleUses(toks []token, sp [2]int) [][2]int {
	o := sp[0]
	if !toks[o].is("(") || o < 3 {
		return nil
	}
	k := o - 1
	for k > 0 && (toks[k].kind == tIdent || toks[k].is(".") || toks[k].is("::")) {
		k--
	}
	if toks[k].kind == tIdent && toks[k].text == "new" {
		k--
	}
	if k < 1 || !(toks[k].is("=") || toks[k].is(":=")) || toks[k-1].kind != tIdent {
		return nil
	}
	name := toks[k-1].text
	if len(name) < 2 {
		return nil
	}
	var out [][2]int
	line := toks[sp[1]].line
	for j := sp[1] + 1; j < len(toks) && toks[j].line-line <= 40; j++ {
		if toks[j].kind != tIdent || toks[j].text != name {
			continue
		}
		if j+1 < len(toks) && (toks[j+1].is("=") || toks[j+1].is(":=")) && !(j+2 < len(toks) && toks[j+2].is("=")) {
			break
		}
		// h.method(...) or f(h, ...)
		if j+3 < len(toks) && (toks[j+1].is(".") || toks[j+1].is("->")) && toks[j+3].is("(") {
			out = append(out, [2]int{j + 3, matchClose(toks, j+3)})
			continue
		}
		if e := enclosingOpen(toks, j); e >= 0 && toks[e].is("(") {
			out = append(out, [2]int{e, matchClose(toks, e)})
		}
	}
	return out
}
