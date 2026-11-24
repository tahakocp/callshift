package scan

import (
	"fmt"
	"strings"

	"github.com/tahakocp/callshift/internal/change"
)

type changeIndex struct {
	byOp map[string][]change.Change
}

func newIndex(cs *change.Set) *changeIndex {
	ix := &changeIndex{byOp: map[string][]change.Change{}}
	for _, c := range cs.Changes {
		k := c.Method + " " + c.Path
		ix.byOp[k] = append(ix.byOp[k], c)
	}
	return ix
}

// Names too common to tie a property read to one response.
var genericNames = map[string]bool{"id": true, "data": true, "type": true, "name": true, "object": true, "status": true,
	"created": true, "url": true, "message": true, "error": true, "value": true, "text": true, "items": true, "content": true,
	"results": true, "result": true, "count": true, "total": true, "length": true, "body": true, "json": true, "then": true,
	"model": true, "role": true, "index": true, "size": true, "code": true, "key": true, "title": true, "description": true, "user": true, "input": true, "prompt": true, "format": true, "mode": true, "style": true, "seed": true, "image": true, "file": true}

func check(file, src string, lines []int, toks []token, c *call, m *matcher, ix *changeIndex, goTags []key) []Finding {
	pm := c.paths[0]
	methods := m.methods[pm.path]
	if len(c.paths) > 1 && c.method != "" {
		for _, p := range c.paths {
			if contains(m.methods[p.path], c.method) {
				pm = p
				methods = m.methods[p.path]
				break
			}
		}
	}
	method, guessed := c.method, c.guessed
	switch {
	case method != "" && contains(methods, method):
	case method != "":
		return nil
	case len(methods) == 1:
		method, guessed = methods[0], false
	default:
		guessed = true
		pref := []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
		if c.bodySeen {
			pref = []string{"POST", "PUT", "PATCH", "GET", "DELETE"}
		}
		for _, p := range pref {
			if contains(methods, p) {
				method = p
				break
			}
		}
	}
	changes := ix.byOp[method+" "+pm.path]
	if len(changes) == 0 {
		return nil
	}
	conf := c.conf
	if guessed && conf > Medium {
		conf = Medium
	}
	if len(c.paths) > 1 && conf > Low {
		conf--
	}
	urlOff := c.urlTok.off
	for _, s := range c.url.segs {
		if s.off >= 0 {
			urlOff = s.off - 1
			break
		}
	}
	if urlOff < c.urlTok.off {
		urlOff = c.urlTok.off
	}
	var out []Finding
	add := func(off int, ch change.Change, lvl int, msg string, fix *Edit) {
		if lvl > conf {
			lvl = conf
		}
		line, col := position(lines, off)
		out = append(out, Finding{File: file, Line: line, Col: col, Kind: ch.Kind, Severity: ch.Severity, Level: lvl,
			Confidence: ConfidenceName(lvl), Method: method, Path: pm.path, MethodGuessed: guessed, Name: ch.Name,
			Message: msg, Snippet: lineText(src, lines, line), Fix: fix, Change: ch})
	}
	keysNamed := func(name string) []key {
		var ks []key
		for _, k := range c.keys {
			if k.name == name {
				ks = append(ks, k)
			}
		}
		return ks
	}
	for _, ch := range changes {
		op := ch.Method + " " + ch.Path
		switch ch.Kind {
		case change.OpRemoved:
			add(urlOff, ch, High, withHint(op+" was removed from the API", ch.Hint), nil)
		case change.OpDeprecated:
			add(urlOff, ch, High, withHint(op+" is deprecated", ch.Hint), nil)
		case change.OpMoved:
			msg := fmt.Sprintf("%s moved to %s", op, ch.NewPath)
			if strings.Contains(ch.Basis, "method ") {
				msg += " (" + ch.Basis + ")"
			}
			add(urlOff, ch, High, msg, movedFix(src, c.url, ch))
		case change.ParamRemoved, change.ParamRenamed, change.ParamDeprecated, change.ParamRequired:
			ks := keysNamed(ch.Name)
			if ch.In == "header" {
				for _, t := range c.strs {
					if strings.EqualFold(t.literal(), ch.Name) {
						ks = append(ks, key{name: ch.Name, off: t.off + 1, exact: false})
					}
				}
			}
			label := ch.In + " parameter `" + ch.Name + "`"
			if ch.Kind == change.ParamRequired {
				if len(ks) == 0 {
					lvl := Low
					if len(c.url.query) > 0 {
						lvl = Medium
					}
					add(urlOff, ch, lvl, fmt.Sprintf("%s now requires %s", op, label), nil)
				}
				continue
			}
			for _, k := range ks {
				switch ch.Kind {
				case change.ParamRemoved:
					add(k.off, ch, High, withHint(label+" was removed from "+op, ch.Hint), nil)
				case change.ParamRenamed:
					add(k.off, ch, High, fmt.Sprintf("%s is now `%s` (%s)", label, ch.NewName, ch.Basis), safe(ch, rename(src, k, ch.NewName)))
				case change.ParamDeprecated:
					var fix *Edit
					if ch.Hint != "" {
						fix = safe(ch, rename(src, k, ch.Hint))
					}
					add(k.off, ch, High, withHint(label+" is deprecated", ch.Hint), fix)
				}
			}
		case change.FieldRemoved, change.FieldRenamed, change.FieldDeprecated, change.FieldType, change.EnumRemoved:
			leaf := ch.Leaf()
			ks := keysNamed(leaf)
			label := "request field `" + ch.Name + "`"
			for _, k := range ks {
				lvl := High
				if strings.Contains(ch.Name, ".") || k.weak {
					lvl = Medium // nested, or outside the call itself
				}
				if k.weak && genericNames[strings.ToLower(leaf)] || k.config {
					lvl = Low // a common word found outside the call, or a settings object
				}
				switch ch.Kind {
				case change.FieldRemoved:
					add(k.off, ch, lvl, withHint(label+" was removed from "+op, ch.Hint), nil)
				case change.FieldRenamed:
					add(k.off, ch, lvl, fmt.Sprintf("%s is now `%s` (%s)", label, ch.NewName, ch.Basis), safe(ch, rename(src, k, change.Leaf(ch.NewName))))
				case change.FieldDeprecated:
					var fix *Edit
					if ch.Hint != "" {
						fix = safe(ch, rename(src, k, change.Leaf(ch.Hint)))
					}
					add(k.off, ch, lvl, withHint(label+" is deprecated", ch.Hint), fix)
				case change.FieldType:
					add(k.off, ch, Medium, fmt.Sprintf("%s changed type from %s to %s", label, ch.OldType, ch.NewType), nil)
				case change.EnumRemoved:
					if k.value == nil {
						continue
					}
					v := k.value.literal()
					if contains(ch.Values, v) {
						off := k.value.off + 1
						if len(k.value.parts) == 1 && k.value.parts[0].off >= 0 {
							off = k.value.parts[0].off
						}
						add(off, ch, lvl, fmt.Sprintf("`%s` is no longer an allowed value of %s", v, label), nil)
					}
				}
			}
		case change.FieldRequired:
			if strings.Contains(ch.Name, ".") {
				parent := strings.SplitN(ch.Name, ".", 2)[0]
				parent = strings.TrimSuffix(parent, "[]")
				if len(keysNamed(parent)) == 0 {
					continue
				}
			}
			if len(keysNamed(ch.Leaf())) == 0 {
				lvl := Low
				if c.bodySeen && !c.config {
					lvl = Medium
				}
				add(urlOff, ch, lvl, fmt.Sprintf("%s now requires request field `%s`", op, ch.Name), nil)
			}
		case change.RespFieldRemoved, change.RespFieldRenamed:
			leaf := ch.Leaf()
			if len(leaf) < 4 || genericNames[strings.ToLower(leaf)] {
				continue
			}
			for _, off := range reads(toks, c.span[1], leaf) {
				msg := "response field `" + ch.Name + "` was removed; this line may read it"
				var fix *Edit
				if ch.Kind == change.RespFieldRenamed {
					msg = fmt.Sprintf("response field `%s` is now `%s`; this line may read it", ch.Name, ch.NewName)
				}
				add(off, ch, Low, msg, fix)
			}
			for _, t := range goTags {
				if t.name == leaf {
					add(t.off, ch, Low, "response field `"+ch.Name+"` was removed; a struct tag still decodes it", nil)
				}
			}
		}
	}
	return dedupe(out)
}

// reads finds property reads of name after token k, within 80 lines.
func reads(toks []token, k int, name string) []int {
	var out []int
	if k >= len(toks) {
		return nil
	}
	line := toks[k].line
	for j := k + 1; j < len(toks) && toks[j].line-line <= 80; j++ {
		t := &toks[j]
		if t.kind == tIdent && t.text == name && j > 0 && (toks[j-1].is(".") || toks[j-1].is("?.")) {
			out = append(out, t.off)
		}
		if t.kind == tStr && t.literal() == name && j > 0 && toks[j-1].is("[") && j+1 < len(toks) && toks[j+1].is("]") {
			out = append(out, t.off+1)
		}
	}
	return out
}

func rename(src string, k key, newName string) *Edit {
	// Keys found outside the call itself are reported but never rewritten.
	if !k.exact || k.weak || k.off < 0 || newName == "" || k.off+len(k.name) > len(src) {
		return nil
	}
	if src[k.off:k.off+len(k.name)] != k.name {
		return nil
	}
	return &Edit{Off: k.off, End: k.off + len(k.name), New: newName, Old: k.name}
}

// movedFix rewrites literal path segments when an operation keeps its
// shape but changes a fixed segment, e.g. /fine-tunes -> /fine_tuning/jobs
// is not fixable (different length) but /v1/foo/{id} -> /v1/bar/{id} is.
func movedFix(src string, u *urlRef, ch change.Change) *Edit {
	oldS := splitPath(ch.Path)
	newS := splitPath(ch.NewPath)
	if len(oldS) != len(newS) || len(u.segs) < len(oldS) {
		return nil
	}
	tail := u.segs[len(u.segs)-len(oldS):]
	var edit *Edit
	for i := range oldS {
		if oldS[i] == newS[i] {
			continue
		}
		if isParam(oldS[i]) && isParam(newS[i]) {
			continue
		}
		if isParam(oldS[i]) || isParam(newS[i]) {
			return nil
		}
		s := tail[i]
		if s.hole || s.tmpl || s.partial || s.off < 0 || s.text != oldS[i] {
			return nil
		}
		if edit != nil {
			return nil // more than one segment changed: leave it to a human
		}
		if s.off+len(s.text) > len(src) || src[s.off:s.off+len(s.text)] != s.text {
			return nil
		}
		edit = &Edit{Off: s.off, End: s.off + len(s.text), New: newS[i], Old: s.text}
	}
	return edit
}

// safe drops a rename when the replacement has a different shape: renaming
// `functions` to `tools` would compile and still be wrong.
func safe(ch change.Change, e *Edit) *Edit {
	if !ch.SafeRename || e == nil {
		return nil
	}
	return e
}

func withHint(msg, hint string) string {
	if hint == "" {
		return msg
	}
	return msg + "; use `" + hint + "`"
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func dedupe(fs []Finding) []Finding {
	seen := map[string]bool{}
	out := fs[:0]
	for _, f := range fs {
		k := fmt.Sprintf("%d:%d:%s:%s", f.Line, f.Col, f.Kind, f.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}
