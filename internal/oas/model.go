package oas

// API is the flat view of one spec version.
type API struct {
	Title      string       `json:"title,omitempty"`
	Version    string       `json:"format,omitempty"`
	APIVersion string       `json:"version,omitempty"`
	Hosts      []string     `json:"hosts,omitempty"`
	BasePaths  []string     `json:"base_paths,omitempty"`
	Ops        []*Operation `json:"operations"`
}

// Operation is one method on one path template.
type Operation struct {
	Method      string  `json:"method"`
	Path        string  `json:"path"`
	ID          string  `json:"operation_id,omitempty"`
	Deprecated  bool    `json:"deprecated,omitempty"`
	Summary     string  `json:"-"`
	Description string  `json:"-"`
	Params      []Param `json:"-"`
	Request     []Field `json:"-"`
	Response    []Field `json:"-"`
}

// Key is "METHOD /path".
func (o *Operation) Key() string { return o.Method + " " + o.Path }

// Param is a path, query, header or cookie parameter.
type Param struct {
	Name        string
	In          string
	Type        string
	Required    bool
	Deprecated  bool
	Description string
}

// Field is one property inside a request or response body. Path is dotted,
// with "[]" for array items: "messages[].content".
type Field struct {
	Path        string
	Name        string
	Type        string
	Required    bool
	Deprecated  bool
	Description string
	Enum        []string
}

// Op finds an operation by method and path template.
func (a *API) Op(method, path string) *Operation {
	for _, o := range a.Ops {
		if o.Method == method && o.Path == path {
			return o
		}
	}
	return nil
}
