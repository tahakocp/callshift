// Package scan finds HTTP calls in source code and checks them against a
// change set. It works on tokens, not on full syntax trees, so one small
// lexer covers JavaScript/TypeScript, Python, Go, Ruby, Java/Kotlin, C#,
// PHP, Swift, Rust, shell scripts and CI YAML.
package scan

import (
	"path/filepath"
	"strings"
)

type tokKind uint8

const (
	tIdent tokKind = iota
	tStr
	tPunct
	tNum
)

// part is a piece of a string literal: literal text or an interpolation hole.
type part struct {
	hole bool
	text string
	off  int // byte offset of text in the file, -1 when not exact (escapes)
}

type token struct {
	kind  tokKind
	text  string // identifier, punctuation, or raw source of a string
	parts []part // for strings
	off   int    // start offset in file
	end   int
	line  int
}

func (t *token) is(p string) bool { return t.kind == tPunct && t.text == p }

// literal returns the string content with holes shown as \x00.
func (t *token) literal() string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.hole {
			b.WriteByte(0)
		} else {
			b.WriteString(p.text)
		}
	}
	return b.String()
}

type lang struct {
	name        string
	lineComment []string
	block       bool // /* */
	quotes      string
	backtick    int // 0 none, 1 template with ${}, 2 raw string (Go), 3 command (shell)
	pyPrefix    bool
	holes       []string // opening markers inside double-quoted strings
	dollarIdent bool     // "$name" is a hole
	bareWords   bool     // shell/YAML: unquoted words with :// or leading / are strings
	hashComment bool
}

var (
	langJS     = &lang{name: "js", lineComment: []string{"//"}, block: true, quotes: `"'`, backtick: 1}
	langGo     = &lang{name: "go", lineComment: []string{"//"}, block: true, quotes: `"'`, backtick: 2}
	langPy     = &lang{name: "python", hashComment: true, quotes: `"'`, pyPrefix: true}
	langRuby   = &lang{name: "ruby", hashComment: true, quotes: `"'`, holes: []string{"#{"}, backtick: 3}
	langJava   = &lang{name: "java", lineComment: []string{"//"}, block: true, quotes: `"'`, holes: []string{"${"}, dollarIdent: true}
	langCS     = &lang{name: "csharp", lineComment: []string{"//"}, block: true, quotes: `"'`, holes: []string{"{"}}
	langSwift  = &lang{name: "swift", lineComment: []string{"//"}, block: true, quotes: `"`, holes: []string{`\(`}}
	langRust   = &lang{name: "rust", lineComment: []string{"//"}, block: true, quotes: `"`}
	langPHP    = &lang{name: "php", lineComment: []string{"//", "#"}, block: true, quotes: `"'`, holes: []string{"{$", "${"}, dollarIdent: true}
	langShell  = &lang{name: "shell", hashComment: true, quotes: `"'`, holes: []string{"${", "$("}, dollarIdent: true, bareWords: true, backtick: 3}
	langYAML   = &lang{name: "yaml", hashComment: true, quotes: `"'`, holes: []string{"${{", "${", "$("}, dollarIdent: true, bareWords: true}
	langDart   = &lang{name: "dart", lineComment: []string{"//"}, block: true, quotes: `"'`, holes: []string{"${"}, dollarIdent: true}
	langCFamly = &lang{name: "c", lineComment: []string{"//"}, block: true, quotes: `"'`}
)

var byExt = map[string]*lang{
	".js": langJS, ".jsx": langJS, ".mjs": langJS, ".cjs": langJS, ".ts": langJS, ".tsx": langJS, ".mts": langJS, ".cts": langJS, ".vue": langJS, ".svelte": langJS, ".astro": langJS,
	".go": langGo,
	".py": langPy, ".pyi": langPy, ".ipynb": nil,
	".rb": langRuby, ".rake": langRuby,
	".java": langJava, ".kt": langJava, ".kts": langJava, ".scala": langJava, ".groovy": langJava,
	".cs":    langCS,
	".swift": langSwift,
	".rs":    langRust,
	".php":   langPHP,
	".sh":    langShell, ".bash": langShell, ".zsh": langShell,
	".yml": langYAML, ".yaml": langYAML,
	".dart": langDart,
	".c":    langCFamly, ".cc": langCFamly, ".cpp": langCFamly, ".h": langCFamly, ".hpp": langCFamly, ".m": langCFamly,
}

func langFor(path string) *lang {
	return byExt[strings.ToLower(filepath.Ext(path))]
}

type lexer struct {
	src  string
	i    int
	line int
	l    *lang
	toks []token
}

func lex(src string, l *lang) []token {
	x := &lexer{src: src, line: 1, l: l}
	x.run()
	return x.toks
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

func (x *lexer) run() {
	s := x.src
	for x.i < len(s) {
		c := s[x.i]
		switch {
		case c == '\n':
			x.line++
			x.i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			x.i++
		case x.l.hashComment && c == '#' && !(x.l == langShell && x.i > 0 && isIdent(s[x.i-1])):
			x.skipLine()
		case x.l.block && strings.HasPrefix(s[x.i:], "/*"):
			x.skipBlock()
		case x.lineComment():
			x.skipLine()
		case x.l.pyPrefix && x.pyString():
		case strings.IndexByte(x.l.quotes, c) >= 0:
			if c == '\'' && x.l != langPy && x.l != langShell && x.l != langYAML && x.l != langRuby && x.l != langPHP && x.l != langJS && x.l != langDart {
				x.charLit()
				continue
			}
			x.str(c, x.i, x.i+1, x.holesFor(c), false)
		case c == '`' && x.l.backtick == 1:
			x.str('`', x.i, x.i+1, []string{"${"}, false)
		case c == '`' && x.l.backtick == 2:
			x.rawStr('`', x.i, x.i+1)
		case c == '`' && x.l.backtick == 3:
			x.i++ // command substitution: lex the inside as code
		case c == '@' && x.l == langCS && x.i+1 < len(s) && s[x.i+1] == '"':
			x.str('"', x.i, x.i+2, nil, true)
		case c == '$' && x.l == langCS && x.i+1 < len(s) && s[x.i+1] == '"':
			x.str('"', x.i, x.i+2, []string{"{"}, false)
		case x.l == langRust && c == 'r' && x.i+1 < len(s) && (s[x.i+1] == '"' || s[x.i+1] == '#'):
			x.rustRaw()
		case x.l.bareWords && x.bareWord():
		case isIdentStart(c):
			j := x.i + 1
			for j < len(s) && isIdent(s[j]) {
				j++
			}
			x.toks = append(x.toks, token{kind: tIdent, text: s[x.i:j], off: x.i, end: j, line: x.line})
			x.i = j
		case c >= '0' && c <= '9':
			j := x.i + 1
			for j < len(s) && (isIdent(s[j]) || s[j] == '.') {
				j++
			}
			x.toks = append(x.toks, token{kind: tNum, text: s[x.i:j], off: x.i, end: j, line: x.line})
			x.i = j
		default:
			n := 1
			for _, op := range []string{"=>", ":=", "::", "->", "==", "!=", "<<", "${{"} {
				if strings.HasPrefix(s[x.i:], op) {
					n = len(op)
					break
				}
			}
			x.toks = append(x.toks, token{kind: tPunct, text: s[x.i : x.i+n], off: x.i, end: x.i + n, line: x.line})
			x.i += n
		}
	}
}

func (x *lexer) holesFor(q byte) []string {
	if q == '\'' && (x.l == langRuby || x.l == langShell || x.l == langPHP || x.l == langYAML) {
		return nil
	}
	return x.l.holes
}

func (x *lexer) lineComment() bool {
	for _, p := range x.l.lineComment {
		if strings.HasPrefix(x.src[x.i:], p) {
			// "//" inside a URL is never reached here because strings are
			// consumed first; but a PHP "#" must not start inside "#{".
			return true
		}
	}
	return false
}

func (x *lexer) skipLine() {
	j := strings.IndexByte(x.src[x.i:], '\n')
	if j < 0 {
		x.i = len(x.src)
		return
	}
	x.i += j
}

func (x *lexer) skipBlock() {
	j := strings.Index(x.src[x.i+2:], "*/")
	end := len(x.src)
	if j >= 0 {
		end = x.i + 2 + j + 2
	}
	x.line += strings.Count(x.src[x.i:end], "\n")
	x.i = end
}

func (x *lexer) charLit() {
	// 'a' or '\n' in C-like languages. Consume conservatively.
	s := x.src
	j := x.i + 1
	for j < len(s) && j < x.i+12 && s[j] != '\'' && s[j] != '\n' {
		if s[j] == '\\' {
			j++
		}
		j++
	}
	if j < len(s) && s[j] == '\'' {
		j++
	}
	x.toks = append(x.toks, token{kind: tNum, text: s[x.i:j], off: x.i, end: j, line: x.line})
	x.i = j
}

// pyString handles Python string prefixes and triple quotes.
func (x *lexer) pyString() bool {
	s := x.src
	j := x.i
	for j < len(s) && j-x.i < 3 && strings.IndexByte("rRbBfFuU", s[j]) >= 0 {
		j++
	}
	if j >= len(s) || (s[j] != '"' && s[j] != '\'') {
		return false
	}
	if j > x.i && x.i > 0 && isIdent(s[x.i-1]) {
		return false
	}
	prefix := strings.ToLower(s[x.i:j])
	raw := strings.Contains(prefix, "r")
	var holes []string
	if strings.Contains(prefix, "f") {
		holes = []string{"{"}
	}
	q := s[j]
	if strings.HasPrefix(s[j:], strings.Repeat(string(q), 3)) {
		x.tripleStr(x.i, j+3, string(q)+string(q)+string(q), holes, raw)
		return true
	}
	x.str(q, x.i, j+1, holes, raw)
	return true
}

// str reads a quoted string that started at start with content at body.
func (x *lexer) str(q byte, start, body int, holes []string, raw bool) {
	s := x.src
	t := token{kind: tStr, off: start, line: x.line}
	j := body
	litStart := j
	var lit strings.Builder
	exact := true
	flush := func(at int) {
		if lit.Len() > 0 || at > litStart {
			off := litStart
			if !exact {
				off = -1
			}
			t.parts = append(t.parts, part{text: lit.String(), off: off})
		}
		lit.Reset()
		exact = true
	}
	for j < len(s) {
		c := s[j]
		if c == q {
			if raw && q == '"' && x.l == langCS && j+1 < len(s) && s[j+1] == '"' { // @"" escape
				lit.WriteByte('"')
				exact = false
				j += 2
				continue
			}
			break
		}
		if c == '\n' && q != '`' && x.l != langShell && x.l != langRuby && x.l != langPHP {
			break // unterminated
		}
		if c == '\n' {
			x.line++
		}
		if c == '\\' && !raw && j+1 < len(s) {
			if h := matchHole(s[j:], holes); h == `\(` {
				flush(j)
				j = x.skipHole(j+2, '(', ')')
				t.parts = append(t.parts, part{hole: true})
				litStart = j
				continue
			}
			lit.WriteByte(unescape(s[j+1]))
			exact = false
			j += 2
			continue
		}
		if h := matchHole(s[j:], holes); h != "" && h != `\(` {
			if h == "{" && j+1 < len(s) && s[j+1] == '{' { // {{ escape in f-strings / C#
				lit.WriteByte('{')
				exact = false
				j += 2
				continue
			}
			flush(j)
			open := h[len(h)-1]
			closeCh := byte('}')
			if open == '(' {
				closeCh = ')'
			}
			j = x.skipHole(j+len(h), open, closeCh)
			t.parts = append(t.parts, part{hole: true})
			litStart = j
			continue
		}
		if c == '$' && x.l.dollarIdent && j+1 < len(s) && (isIdentStart(s[j+1]) && s[j+1] != '$') && q != '\'' {
			flush(j)
			k := j + 1
			for k < len(s) && isIdent(s[k]) && s[k] != '$' {
				k++
			}
			t.parts = append(t.parts, part{hole: true})
			j = k
			litStart = j
			continue
		}
		lit.WriteByte(c)
		j++
	}
	flush(j)
	if j < len(s) {
		j++
	}
	t.end = j
	t.text = s[start:j]
	x.toks = append(x.toks, t)
	x.i = j
}

func (x *lexer) tripleStr(start, body int, q string, holes []string, raw bool) {
	s := x.src
	end := strings.Index(s[body:], q)
	if end < 0 {
		end = len(s) - body
	}
	content := s[body : body+end]
	t := token{kind: tStr, off: start, line: x.line, end: body + end + len(q)}
	if t.end > len(s) {
		t.end = len(s)
	}
	t.text = s[start:t.end]
	t.parts = splitHoles(content, body, holes)
	x.line += strings.Count(content, "\n")
	_ = raw
	x.toks = append(x.toks, t)
	x.i = t.end
}

func splitHoles(content string, base int, holes []string) []part {
	var out []part
	last := 0
	for k := 0; k < len(content); k++ {
		if h := matchHole(content[k:], holes); h != "" {
			if h == "{" && k+1 < len(content) && content[k+1] == '{' {
				k++
				continue
			}
			out = append(out, part{text: content[last:k], off: base + last})
			e := strings.IndexByte(content[k:], '}')
			if e < 0 {
				e = len(content) - k - 1
			}
			out = append(out, part{hole: true})
			k += e
			last = k + 1
		}
	}
	if last <= len(content) {
		out = append(out, part{text: content[last:], off: base + last})
	}
	return out
}

func (x *lexer) rawStr(q byte, start, body int) {
	s := x.src
	end := strings.IndexByte(s[body:], q)
	if end < 0 {
		end = len(s) - body
	}
	content := s[body : body+end]
	t := token{kind: tStr, off: start, line: x.line, end: body + end + 1}
	if t.end > len(s) {
		t.end = len(s)
	}
	t.text = s[start:t.end]
	t.parts = []part{{text: content, off: body}}
	x.line += strings.Count(content, "\n")
	x.toks = append(x.toks, t)
	x.i = t.end
}

func (x *lexer) rustRaw() {
	s := x.src
	j := x.i + 1
	hashes := 0
	for j < len(s) && s[j] == '#' {
		hashes++
		j++
	}
	if j >= len(s) || s[j] != '"' {
		// not a raw string, plain identifier r
		x.toks = append(x.toks, token{kind: tIdent, text: "r", off: x.i, end: x.i + 1, line: x.line})
		x.i++
		return
	}
	closing := "\"" + strings.Repeat("#", hashes)
	body := j + 1
	end := strings.Index(s[body:], closing)
	if end < 0 {
		end = len(s) - body
	}
	content := s[body : body+end]
	t := token{kind: tStr, off: x.i, line: x.line, end: body + end + len(closing)}
	if t.end > len(s) {
		t.end = len(s)
	}
	t.text = s[x.i:t.end]
	t.parts = []part{{text: content, off: body}}
	x.line += strings.Count(content, "\n")
	x.toks = append(x.toks, t)
	x.i = t.end
}

// bareWord turns an unquoted URL or absolute path in shell/YAML into a
// string token, so `curl https://api.example.com/v1/x` is seen as a URL.
func (x *lexer) bareWord() bool {
	s := x.src
	if x.i > 0 && !strings.ContainsRune(" \t\n=:([{,", rune(s[x.i-1])) {
		return false
	}
	j := x.i
	for j < len(s) && !strings.ContainsRune(" \t\r\n\"'`;|&<>(){}[],", rune(s[j])) {
		j++
	}
	// allow ${VAR} and ${{ expr }} inside the word
	for j < len(s) && s[j] == '{' {
		e := strings.IndexByte(s[j:], '}')
		if e < 0 {
			break
		}
		j += e + 1
		for j < len(s) && s[j] == '}' {
			j++
		}
		for j < len(s) && !strings.ContainsRune(" \t\r\n\"'`;|&<>(){}[],", rune(s[j])) {
			j++
		}
	}
	w := s[x.i:j]
	if !(strings.Contains(w, "://") || (strings.HasPrefix(w, "/") && strings.Count(w, "/") >= 2) || (strings.HasPrefix(w, "$") && strings.Contains(w, "/"))) {
		return false
	}
	t := token{kind: tStr, off: x.i, end: j, line: x.line, text: w}
	t.parts = splitShellHoles(w, x.i)
	x.toks = append(x.toks, t)
	x.i = j
	return true
}

func splitShellHoles(w string, base int) []part {
	var out []part
	last := 0
	for k := 0; k < len(w); k++ {
		if w[k] != '$' {
			continue
		}
		e := k + 1
		switch {
		case strings.HasPrefix(w[k:], "${{"):
			if m := strings.Index(w[k:], "}}"); m >= 0 {
				e = k + m + 2
			}
		case strings.HasPrefix(w[k:], "${"):
			if m := strings.IndexByte(w[k:], '}'); m >= 0 {
				e = k + m + 1
			}
		default:
			for e < len(w) && isIdent(w[e]) && w[e] != '$' {
				e++
			}
			if e == k+1 {
				continue
			}
		}
		out = append(out, part{text: w[last:k], off: base + last}, part{hole: true})
		k = e - 1
		last = e
	}
	out = append(out, part{text: w[last:], off: base + last})
	return out
}

func matchHole(s string, holes []string) string {
	for _, h := range holes {
		if strings.HasPrefix(s, h) {
			return h
		}
	}
	return ""
}

func (x *lexer) skipHole(j int, open, closeCh byte) int {
	s := x.src
	depth := 1
	for j < len(s) && depth > 0 {
		switch s[j] {
		case open:
			depth++
		case closeCh:
			depth--
		case '\n':
			x.line++
		case '"', '\'', '`':
			q := s[j]
			j++
			for j < len(s) && s[j] != q && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
		}
		j++
	}
	return j
}

func unescape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case '0':
		return 0
	}
	return c
}
