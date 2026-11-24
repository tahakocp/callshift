// Package change compares two flattened API specs (or reads one spec's
// deprecations) and produces a change set that a scanner can apply to
// client code without needing the specs themselves.
package change

import (
	"regexp"
	"sort"
	"strings"

	"github.com/tahakocp/callshift/internal/oas"
)

// Schema identifies the change set format.
const Schema = "callshift/changes/1"

// Kinds of change. Breaking kinds fail a scan by default.
const (
	OpRemoved        = "op-removed"
	OpMoved          = "op-moved"
	OpDeprecated     = "op-deprecated"
	ParamRemoved     = "param-removed"
	ParamRenamed     = "param-renamed"
	ParamRequired    = "param-required"
	ParamDeprecated  = "param-deprecated"
	FieldRemoved     = "field-removed"
	FieldRenamed     = "field-renamed"
	FieldRequired    = "field-required"
	FieldDeprecated  = "field-deprecated"
	FieldType        = "field-type"
	EnumRemoved      = "enum-removed"
	RespFieldRemoved = "response-field-removed"
	RespFieldRenamed = "response-field-renamed"
)

// Severity of a change.
const (
	Breaking   = "breaking"
	Deprecated = "deprecated"
)

// Change is one thing a client may have to do something about.
type Change struct {
	Kind     string   `json:"kind"`
	Severity string   `json:"severity"`
	Method   string   `json:"method"`
	Path     string   `json:"path"`
	NewPath  string   `json:"new_path,omitempty"`
	In       string   `json:"in,omitempty"`   // query, header, cookie, body, response
	Name     string   `json:"name,omitempty"` // param name or dotted field path
	NewName  string   `json:"new_name,omitempty"`
	OldType  string   `json:"old_type,omitempty"`
	NewType  string   `json:"new_type,omitempty"`
	Values   []string `json:"values,omitempty"`
	Hint     string   `json:"hint,omitempty"` // replacement named in the spec text
	Basis    string   `json:"basis,omitempty"`
	// SafeRename is set when NewName or Hint names a field or parameter of
	// the same type and the same nested shape, so renaming the key in code
	// is a complete fix.
	SafeRename bool `json:"safe_rename,omitempty"`
}

// Leaf is the last segment of a field path ("messages[].content" -> "content").
func (c *Change) Leaf() string { return Leaf(c.Name) }

// Leaf returns the last dotted segment of a field path without "[]".
func Leaf(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		p = p[i+1:]
	}
	return strings.TrimSuffix(p, "[]")
}

// Op is the minimum a scanner needs to recognise a call to an operation.
type Op struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Set is the portable output of a diff.
type Set struct {
	Schema    string   `json:"schema"`
	API       string   `json:"api,omitempty"`
	From      string   `json:"from,omitempty"`
	To        string   `json:"to,omitempty"`
	Hosts     []string `json:"hosts,omitempty"`
	BasePaths []string `json:"base_paths,omitempty"`
	Ops       []Op     `json:"operations"`
	Changes   []Change `json:"changes"`
}

// normPath turns /a/{x}/b into /a/{}/b so renamed path params still match.
var paramRe = regexp.MustCompile(`\{[^}/]*\}`)

func normPath(p string) string { return paramRe.ReplaceAllString(p, "{}") }

func opKey(o *oas.Operation) string { return o.Method + " " + normPath(o.Path) }

// Diff compares two specs. The result lists operations from both versions so
// that code written against the old one can still be recognised.
func Diff(old, nw *oas.API) *Set {
	s := &Set{Schema: Schema, API: firstNonEmpty(nw.Title, old.Title), From: old.APIVersion, To: nw.APIVersion}
	s.Hosts = union(old.Hosts, nw.Hosts)
	s.BasePaths = union(old.BasePaths, nw.BasePaths)
	s.Ops = opList(old, nw)

	newByKey := map[string]*oas.Operation{}
	newByID := map[string]*oas.Operation{}
	for _, o := range nw.Ops {
		newByKey[opKey(o)] = o
		if o.ID != "" {
			newByID[o.ID] = o
		}
	}
	oldKeys := map[string]bool{}
	for _, o := range old.Ops {
		oldKeys[opKey(o)] = true
	}
	for _, o := range old.Ops {
		n := newByKey[opKey(o)]
		if n == nil {
			if m := newByID[o.ID]; o.ID != "" && m != nil && !oldKeys[opKey(m)] {
				s.add(Change{Kind: OpMoved, Severity: Breaking, Method: o.Method, Path: o.Path, NewPath: m.Path, Basis: "same operationId " + o.ID})
				if m.Method != o.Method {
					s.Changes[len(s.Changes)-1].Basis += ", method " + m.Method
				}
				diffOp(s, o, m)
				continue
			}
			s.add(Change{Kind: OpRemoved, Severity: Breaking, Method: o.Method, Path: o.Path, Hint: hint(o.Description)})
			continue
		}
		if n.Deprecated && !o.Deprecated {
			s.add(Change{Kind: OpDeprecated, Severity: Deprecated, Method: o.Method, Path: o.Path, Hint: hint(n.Description)})
		}
		diffOp(s, o, n)
	}
	s.sort()
	return s
}

// Deprecations lists everything a single spec marks as deprecated.
func Deprecations(a *oas.API) *Set {
	s := &Set{Schema: Schema, API: a.Title, To: a.APIVersion, Hosts: a.Hosts, BasePaths: a.BasePaths, Ops: opList(a)}
	for _, o := range a.Ops {
		if o.Deprecated {
			s.add(Change{Kind: OpDeprecated, Severity: Deprecated, Method: o.Method, Path: o.Path, Hint: hint(o.Description)})
		}
		for _, p := range o.Params {
			if p.Deprecated && p.In != "path" {
				c := Change{Kind: ParamDeprecated, Severity: Deprecated, Method: o.Method, Path: o.Path, In: p.In, Name: p.Name}
				c.Hint = replacementParam(p.Description, o.Params, p)
				c.SafeRename = c.Hint != "" && paramType(o.Params, p.In, c.Hint) == p.Type
				s.add(c)
			}
		}
		for _, f := range topmost(o.Request, func(f oas.Field) bool { return f.Deprecated }) {
			c := Change{Kind: FieldDeprecated, Severity: Deprecated, Method: o.Method, Path: o.Path, In: "body", Name: f.Path}
			if r := replacementField(f, o.Request); r != "" {
				c.Hint = r
				c.SafeRename = sameShape(o.Request, f.Path, r)
			}
			s.add(c)
		}
	}
	s.sort()
	return s
}

func diffOp(s *Set, o, n *oas.Operation) {
	base := Change{Method: o.Method, Path: o.Path}

	// Parameters. Path parameters are positional and invisible to callers
	// when renamed, so they are skipped.
	oldP := map[string]oas.Param{}
	newP := map[string]oas.Param{}
	for _, p := range o.Params {
		if p.In != "path" {
			oldP[p.In+":"+p.Name] = p
		}
	}
	for _, p := range n.Params {
		if p.In != "path" {
			newP[p.In+":"+p.Name] = p
		}
	}
	var removedP []oas.Param
	var addedP []oas.Param
	for _, k := range keys(oldP) {
		if _, ok := newP[k]; !ok {
			removedP = append(removedP, oldP[k])
		}
	}
	for _, k := range keys(newP) {
		if _, ok := oldP[k]; !ok {
			addedP = append(addedP, newP[k])
		}
	}
	usedP := map[string]bool{}
	for _, r := range removedP {
		c := base
		c.In, c.Name = r.In, r.Name
		if a, basis := pairParam(r, addedP, usedP); a != nil {
			usedP[a.In+":"+a.Name] = true
			c.Kind, c.Severity, c.NewName, c.Basis = ParamRenamed, Breaking, a.Name, basis
			c.SafeRename = a.Type == r.Type
		} else {
			c.Kind, c.Severity = ParamRemoved, Breaking
			c.Hint = hint(r.Description)
		}
		s.add(c)
	}
	for _, a := range addedP {
		if a.Required && !usedP[a.In+":"+a.Name] {
			c := base
			c.Kind, c.Severity, c.In, c.Name = ParamRequired, Breaking, a.In, a.Name
			c.Basis = "new required parameter"
			s.add(c)
		}
	}
	for k, np := range newP {
		op, ok := oldP[k]
		if !ok {
			continue
		}
		if np.Required && !op.Required {
			c := base
			c.Kind, c.Severity, c.In, c.Name, c.Basis = ParamRequired, Breaking, np.In, np.Name, "parameter became required"
			s.add(c)
		}
		if np.Deprecated && !op.Deprecated {
			c := base
			c.Kind, c.Severity, c.In, c.Name = ParamDeprecated, Deprecated, np.In, np.Name
			c.Hint = replacementParam(np.Description, n.Params, np)
			c.SafeRename = c.Hint != "" && paramType(n.Params, np.In, c.Hint) == np.Type
			s.add(c)
		}
	}

	diffFields(s, base, "body", o.Request, n.Request)
	diffFields(s, base, "response", o.Response, n.Response)
}

func diffFields(s *Set, base Change, in string, oldF, newF []oas.Field) {
	oldM := map[string]oas.Field{}
	newM := map[string]oas.Field{}
	for _, f := range oldF {
		oldM[f.Path] = f
	}
	for _, f := range newF {
		newM[f.Path] = f
	}
	removed := topmost(oldF, func(f oas.Field) bool { _, ok := newM[f.Path]; return !ok })
	added := topmost(newF, func(f oas.Field) bool { _, ok := oldM[f.Path]; return !ok })
	used := map[string]bool{}
	for _, r := range removed {
		c := base
		c.In, c.Name = in, r.Path
		a, basis := pairField(r, added, used)
		switch {
		case a != nil && in == "response":
			used[a.Path] = true
			c.Kind, c.Severity, c.NewName, c.Basis = RespFieldRenamed, Breaking, a.Path, basis
		case a != nil:
			used[a.Path] = true
			c.Kind, c.Severity, c.NewName, c.Basis = FieldRenamed, Breaking, a.Path, basis
			c.SafeRename = shapeOf(oldF, r.Path) == shapeOf(newF, a.Path)
		case in == "response":
			c.Kind, c.Severity = RespFieldRemoved, Breaking
		default:
			c.Kind, c.Severity = FieldRemoved, Breaking
			if r.Deprecated {
				c.Hint = replacementField(r, oldF)
			}
		}
		s.add(c)
	}
	if in == "response" {
		return
	}
	for _, a := range added {
		parent := parentOf(a.Path)
		if a.Required && !used[a.Path] && (parent == "" || inMap(oldM, parent)) {
			c := base
			c.Kind, c.Severity, c.In, c.Name, c.Basis = FieldRequired, Breaking, in, a.Path, "new required field"
			s.add(c)
		}
	}
	for _, p := range sortedFieldKeys(newM) {
		nf := newM[p]
		of, ok := oldM[p]
		if !ok {
			continue
		}
		if nf.Required && !of.Required {
			c := base
			c.Kind, c.Severity, c.In, c.Name, c.Basis = FieldRequired, Breaking, in, p, "field became required"
			s.add(c)
		}
		if nf.Deprecated && !of.Deprecated {
			c := base
			c.Kind, c.Severity, c.In, c.Name = FieldDeprecated, Deprecated, in, p
			c.Hint = replacementField(nf, newF)
			c.SafeRename = c.Hint != "" && sameShape(newF, p, c.Hint)
			s.add(c)
		}
		if gone := minus(of.Enum, nf.Enum); len(of.Enum) > 0 && len(nf.Enum) > 0 && len(gone) > 0 && !openEnum(nf) {
			c := base
			c.Kind, c.Severity, c.In, c.Name, c.Values = EnumRemoved, Breaking, in, p, gone
			s.add(c)
		}
		if typeNarrowed(of.Type, nf.Type) {
			c := base
			c.Kind, c.Severity, c.In, c.Name, c.OldType, c.NewType = FieldType, Breaking, in, p, of.Type, nf.Type
			s.add(c)
		}
	}
}

// openEnum reports whether the field also accepts any string, as in
// anyOf: [{type: string}, {enum: [...]}]. Removing a value from such a list
// is documentation, not a contract change.
func openEnum(f oas.Field) bool {
	parts := strings.Split(f.Type, "|")
	n := 0
	for _, p := range parts {
		if p == "string" {
			n++
		}
	}
	return n >= 2
}

// typeNarrowed is true when every type the old field accepted is gone.
func typeNarrowed(oldT, newT string) bool {
	if oldT == "" || newT == "" || oldT == newT {
		return false
	}
	nt := map[string]bool{}
	for _, t := range strings.Split(newT, "|") {
		nt[t] = true
	}
	if nt["number"] {
		nt["integer"] = true
	}
	for _, t := range strings.Split(oldT, "|") {
		if nt[t] {
			return false
		}
	}
	return true
}

// topmost keeps fields that match pred and whose parent does not also match,
// so a removed object is reported once rather than once per property.
func topmost(fs []oas.Field, pred func(oas.Field) bool) []oas.Field {
	hit := map[string]bool{}
	for _, f := range fs {
		if pred(f) {
			hit[f.Path] = true
		}
	}
	var out []oas.Field
	for _, f := range fs {
		if !hit[f.Path] {
			continue
		}
		covered := false
		for p := parentOf(f.Path); p != ""; p = parentOf(p) {
			if hit[p] {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, f)
		}
	}
	return out
}

func parentOf(p string) string {
	i := strings.LastIndex(p, ".")
	if i < 0 {
		return ""
	}
	return strings.TrimSuffix(p[:i], "[]")
}

func inMap(m map[string]oas.Field, p string) bool {
	if _, ok := m[p]; ok {
		return true
	}
	_, ok := m[p+"[]"]
	return ok
}

func (s *Set) add(c Change) { s.Changes = append(s.Changes, c) }

func (s *Set) sort() {
	sort.SliceStable(s.Changes, func(i, j int) bool {
		a, b := s.Changes[i], s.Changes[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		if a.In != b.In {
			return a.In < b.In
		}
		return a.Name < b.Name
	})
}

func opList(apis ...*oas.API) []Op {
	seen := map[string]bool{}
	var out []Op
	for _, a := range apis {
		for _, o := range a.Ops {
			k := o.Method + " " + o.Path
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, Op{Method: o.Method, Path: o.Path})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string{}, a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func minus(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedFieldKeys(m map[string]oas.Field) []string { return keys(m) }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// shapeOf describes a field's type and everything nested under it, relative
// to the field, so two fields can be compared for a drop-in rename.
func shapeOf(fs []oas.Field, path string) string {
	var parts []string
	for _, f := range fs {
		switch {
		case f.Path == path:
			parts = append(parts, "="+f.Type)
		case strings.HasPrefix(f.Path, path+".") || strings.HasPrefix(f.Path, path+"[]"):
			parts = append(parts, f.Path[len(path):]+"="+f.Type)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func sameShape(fs []oas.Field, a, b string) bool {
	sa := shapeOf(fs, a)
	return sa != "" && !strings.HasPrefix(sa, "=;") && sa != "=" && sa == shapeOf(fs, b)
}

func paramType(ps []oas.Param, in, name string) string {
	for _, p := range ps {
		if p.In == in && p.Name == name {
			return p.Type
		}
	}
	return "\x00"
}
