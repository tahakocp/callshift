// Package oas reads OpenAPI 3.x and Swagger 2.0 documents into a flat model
// that is easy to compare: every operation with its parameters and the
// request and response fields it accepts or returns, as dotted paths.
package oas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads a spec from a file. JSON and YAML are both accepted.
func Load(path string) (*API, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse builds the flat model from raw JSON or YAML bytes.
func Parse(b []byte) (*API, error) {
	var root any
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(trimmed, &root); err != nil {
			return nil, fmt.Errorf("json: %w", err)
		}
	} else {
		var n yaml.Node
		if err := yaml.Unmarshal(b, &n); err != nil {
			return nil, fmt.Errorf("yaml: %w", err)
		}
		root = fromNode(&n)
	}
	doc, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("spec root is not an object")
	}
	r := &reader{doc: doc}
	return r.build()
}

// fromNode converts a yaml.Node tree to plain Go values. Doing it by hand
// (instead of decoding into any) keeps keys as strings even when they look
// like numbers, which matters for response codes such as 200.
func fromNode(n *yaml.Node) any {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return fromNode(n.Content[0])
	case yaml.AliasNode:
		return fromNode(n.Alias)
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			v := n.Content[i+1]
			if k.Value == "<<" && k.Tag == "!!merge" {
				if mm, ok := fromNode(v).(map[string]any); ok {
					for kk, vv := range mm {
						if _, exists := m[kk]; !exists {
							m[kk] = vv
						}
					}
				}
				continue
			}
			m[k.Value] = fromNode(v)
		}
		return m
	case yaml.SequenceNode:
		s := make([]any, len(n.Content))
		for i, c := range n.Content {
			s[i] = fromNode(c)
		}
		return s
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!bool":
			return n.Value == "true" || n.Value == "True" || n.Value == "TRUE"
		case "!!null":
			return nil
		case "!!int", "!!float":
			var f float64
			if _, err := fmt.Sscan(n.Value, &f); err == nil {
				return f
			}
		}
		return n.Value
	}
	return nil
}

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

type reader struct {
	doc map[string]any
}

func (r *reader) build() (*API, error) {
	api := &API{}
	if s, ok := r.doc["swagger"].(string); ok {
		api.Version = "swagger " + s
		host, _ := r.doc["host"].(string)
		base, _ := r.doc["basePath"].(string)
		if host != "" {
			api.Hosts = append(api.Hosts, strings.ToLower(host))
		}
		if base != "" && base != "/" {
			api.BasePaths = append(api.BasePaths, strings.TrimRight(base, "/"))
		}
	} else if s, ok := r.doc["openapi"].(string); ok {
		api.Version = "openapi " + s
	} else {
		return nil, fmt.Errorf("not an OpenAPI or Swagger document (no openapi/swagger key)")
	}
	if info, ok := r.doc["info"].(map[string]any); ok {
		api.Title, _ = info["title"].(string)
		api.APIVersion = fmt.Sprint(info["version"])
	}
	for _, s := range asSlice(r.doc["servers"]) {
		sm, _ := s.(map[string]any)
		raw, _ := sm["url"].(string)
		r.addServer(api, raw, sm)
	}
	paths, _ := r.doc["paths"].(map[string]any)
	keys := sortedKeys(paths)
	for _, p := range keys {
		item, _ := r.deref(paths[p]).(map[string]any)
		if item == nil {
			continue
		}
		shared := asSlice(item["parameters"])
		for _, m := range methods {
			opRaw, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			api.Ops = append(api.Ops, r.operation(p, m, opRaw, shared))
		}
	}
	api.Hosts = uniq(api.Hosts)
	api.BasePaths = uniq(api.BasePaths)
	return api, nil
}

func (r *reader) addServer(api *API, raw string, sm map[string]any) {
	if raw == "" {
		return
	}
	// Substitute server variables with their defaults.
	if vars, ok := sm["variables"].(map[string]any); ok {
		for name, v := range vars {
			vm, _ := v.(map[string]any)
			def := fmt.Sprint(vm["default"])
			raw = strings.ReplaceAll(raw, "{"+name+"}", def)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	if u.Host != "" {
		api.Hosts = append(api.Hosts, strings.ToLower(u.Hostname()))
	}
	if p := strings.TrimRight(u.Path, "/"); p != "" {
		api.BasePaths = append(api.BasePaths, p)
	}
}

func (r *reader) operation(path, method string, raw map[string]any, shared []any) *Operation {
	op := &Operation{Method: strings.ToUpper(method), Path: path}
	op.ID, _ = raw["operationId"].(string)
	op.Deprecated, _ = raw["deprecated"].(bool)
	op.Summary, _ = raw["summary"].(string)
	desc, _ := raw["description"].(string)
	op.Description = desc

	seen := map[string]bool{}
	addParam := func(p any) {
		pm, _ := r.deref(p).(map[string]any)
		if pm == nil {
			return
		}
		name, _ := pm["name"].(string)
		in, _ := pm["in"].(string)
		if name == "" || in == "" {
			return
		}
		key := in + ":" + name
		if seen[key] {
			return
		}
		seen[key] = true
		if in == "body" { // swagger 2 body parameter
			op.Request = r.fields(pm["schema"], "", 0, map[string]bool{})
			return
		}
		if in == "formData" {
			op.Request = append(op.Request, Field{Path: name, Name: name, Type: typeOf(pm), Required: asBool(pm["required"]), Description: str(pm["description"])})
			return
		}
		par := Param{Name: name, In: in, Required: asBool(pm["required"]), Deprecated: asBool(pm["deprecated"]), Description: str(pm["description"])}
		if s, ok := r.deref(pm["schema"]).(map[string]any); ok {
			par.Type = typeOf(s)
			if !par.Deprecated {
				par.Deprecated = asBool(s["deprecated"])
			}
		} else {
			par.Type = typeOf(pm)
		}
		op.Params = append(op.Params, par)
	}
	// Operation-level parameters override path-level ones with the same key.
	for _, p := range asSlice(raw["parameters"]) {
		addParam(p)
	}
	for _, p := range shared {
		addParam(p)
	}
	if rb, ok := r.deref(raw["requestBody"]).(map[string]any); ok {
		op.Request = r.bodyFields(rb["content"])
	}
	if resp, ok := raw["responses"].(map[string]any); ok {
		for _, code := range sortedKeys(resp) {
			if !strings.HasPrefix(code, "2") {
				continue
			}
			rm, _ := r.deref(resp[code]).(map[string]any)
			if rm == nil {
				continue
			}
			s := pickMedia(rm["content"])
			if s == nil {
				s = rm["schema"] // swagger 2
			}
			if s != nil {
				op.Response = mergeFields(op.Response, r.fields(s, "", 0, map[string]bool{}))
			}
		}
	}
	sort.Slice(op.Params, func(i, j int) bool { return op.Params[i].In+op.Params[i].Name < op.Params[j].In+op.Params[j].Name })
	return op
}

// bodyFields merges the request schemas of every JSON or form media type.
// An API may accept multipart with "image" and JSON with "images"; a field
// counts as required only when every media type requires it.
func (r *reader) bodyFields(content any) []Field {
	cm, _ := content.(map[string]any)
	var variants [][]Field
	for _, k := range sortedKeys(cm) {
		if !(strings.Contains(k, "json") || strings.Contains(k, "form")) {
			continue
		}
		m, _ := cm[k].(map[string]any)
		if m == nil || m["schema"] == nil {
			continue
		}
		variants = append(variants, r.fields(m["schema"], "", 0, map[string]bool{}))
	}
	if len(variants) == 0 {
		if s := pickMedia(content); s != nil {
			return r.fields(s, "", 0, map[string]bool{})
		}
		return nil
	}
	if len(variants) == 1 {
		return variants[0]
	}
	reqCount := map[string]int{}
	for _, v := range variants {
		for _, f := range v {
			if f.Required {
				reqCount[f.Path]++
			}
		}
	}
	var out []Field
	for _, v := range variants {
		vv := append([]Field(nil), v...)
		for i := range vv {
			vv[i].Required = false
		}
		out = mergeFields(out, vv)
	}
	for i := range out {
		out[i].Required = reqCount[out[i].Path] == len(variants)
	}
	return out
}

// pickMedia returns the schema of the most JSON-like media type.
func pickMedia(content any) any {
	cm, _ := content.(map[string]any)
	if cm == nil {
		return nil
	}
	pref := []string{"application/json", "multipart/form-data", "application/x-www-form-urlencoded"}
	for _, p := range pref {
		if m, ok := cm[p].(map[string]any); ok && m["schema"] != nil {
			return m["schema"]
		}
	}
	for _, k := range sortedKeys(cm) {
		if strings.Contains(k, "json") {
			if m, ok := cm[k].(map[string]any); ok && m["schema"] != nil {
				return m["schema"]
			}
		}
	}
	return nil
}

const maxDepth = 7

// fields flattens a schema into dotted field paths. Arrays add "[]".
// oneOf/anyOf variants are merged; a field is required only where the
// schema says so on the object that owns it.
func (r *reader) fields(schema any, prefix string, depth int, stack map[string]bool) []Field {
	if depth > maxDepth {
		return nil
	}
	ref := ""
	if m, ok := schema.(map[string]any); ok {
		ref, _ = m["$ref"].(string)
	}
	if ref != "" {
		if stack[ref] {
			return nil
		}
		stack[ref] = true
		defer delete(stack, ref)
	}
	s, _ := r.deref(schema).(map[string]any)
	if s == nil {
		return nil
	}
	var out []Field
	for _, sub := range asSlice(s["allOf"]) {
		out = mergeFields(out, r.fields(sub, prefix, depth+1, stack))
	}
	// A field required inside one oneOf/anyOf variant is not required by
	// the operation as a whole, so variant fields are merged as optional.
	for _, key := range []string{"oneOf", "anyOf"} {
		for _, sub := range asSlice(s[key]) {
			vf := r.fields(sub, prefix, depth+1, stack)
			for i := range vf {
				vf[i].Required = false
			}
			out = mergeFields(out, vf)
		}
	}
	if items, ok := s["items"]; ok && typeOf(s) == "array" {
		out = mergeFields(out, r.fields(items, prefix+"[]", depth+1, stack))
	}
	props, _ := s["properties"].(map[string]any)
	req := map[string]bool{}
	for _, q := range asSlice(s["required"]) {
		if qs, ok := q.(string); ok {
			req[qs] = true
		}
	}
	for _, name := range sortedKeys(props) {
		ps, _ := r.deref(props[name]).(map[string]any)
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		f := Field{Path: path, Name: name, Required: req[name]}
		if ps != nil {
			f.Type = typeOf(ps)
			f.Deprecated = asBool(ps["deprecated"])
			f.Description = str(ps["description"])
			f.Enum = enumOf(ps, r)
		}
		out = mergeFields(out, []Field{f})
		out = mergeFields(out, r.fields(props[name], path, depth+1, stack))
	}
	return out
}

// enumOf collects string enum values, also through anyOf/oneOf wrappers,
// which is how many specs describe "one of these known values or any string".
func enumOf(s map[string]any, r *reader) []string {
	var out []string
	for _, e := range asSlice(s["enum"]) {
		if es, ok := e.(string); ok {
			out = append(out, es)
		}
	}
	if c, ok := s["const"].(string); ok {
		out = append(out, c)
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		for _, sub := range asSlice(s[key]) {
			if sm, ok := r.deref(sub).(map[string]any); ok {
				for _, e := range asSlice(sm["enum"]) {
					if es, ok := e.(string); ok {
						out = append(out, es)
					}
				}
				if c, ok := sm["const"].(string); ok {
					out = append(out, c)
				}
			}
		}
	}
	return uniq(out)
}

func mergeFields(a, b []Field) []Field {
	if len(b) == 0 {
		return a
	}
	idx := make(map[string]int, len(a))
	for i, f := range a {
		idx[f.Path] = i
	}
	for _, f := range b {
		if i, ok := idx[f.Path]; ok {
			g := &a[i]
			g.Required = g.Required || f.Required
			g.Deprecated = g.Deprecated || f.Deprecated
			if g.Type == "" {
				g.Type = f.Type
			} else if f.Type != "" && f.Type != g.Type && !strings.Contains(g.Type, f.Type) {
				g.Type = g.Type + "|" + f.Type
			}
			if g.Description == "" {
				g.Description = f.Description
			}
			g.Enum = uniq(append(g.Enum, f.Enum...))
			continue
		}
		idx[f.Path] = len(a)
		a = append(a, f)
	}
	return a
}

// deref follows local $ref pointers (#/components/...). External refs are
// left unresolved and treated as opaque.
func (r *reader) deref(v any) any {
	for i := 0; i < 32; i++ {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return v
		}
		if !strings.HasPrefix(ref, "#/") {
			return nil
		}
		var cur any = r.doc
		for _, part := range strings.Split(ref[2:], "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			cm, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = cm[part]
		}
		v = cur
	}
	return nil
}

func typeOf(s map[string]any) string {
	switch t := s["type"].(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, x := range t {
			if xs, ok := x.(string); ok && xs != "null" {
				parts = append(parts, xs)
			}
		}
		return strings.Join(parts, "|")
	}
	var alts []string
	for _, key := range []string{"anyOf", "oneOf"} {
		for _, sub := range asSlice(s[key]) {
			if sm, ok := sub.(map[string]any); ok {
				if t := typeOf(sm); t != "" {
					alts = append(alts, t)
				}
			}
		}
	}
	if len(alts) > 0 {
		return strings.Join(uniq(alts), "|")
	}
	if _, ok := s["properties"]; ok {
		return "object"
	}
	if _, ok := s["items"]; ok {
		return "array"
	}
	return ""
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func uniq(s []string) []string {
	if len(s) < 2 {
		return s
	}
	seen := make(map[string]bool, len(s))
	out := s[:0]
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
