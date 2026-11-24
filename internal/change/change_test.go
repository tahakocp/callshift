package change

import (
	"testing"

	"github.com/tahakocp/callshift/internal/oas"
)

func load(t *testing.T, p string) *oas.API {
	t.Helper()
	a, err := oas.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func find(cs *Set, kind, method, path, name string) *Change {
	for i, c := range cs.Changes {
		if c.Kind == kind && c.Method == method && c.Path == path && c.Name == name {
			return &cs.Changes[i]
		}
	}
	return nil
}

func TestDiffShop(t *testing.T) {
	cs := Diff(load(t, "../../testdata/shop-v1.yaml"), load(t, "../../testdata/shop-v2.yaml"))
	cases := []struct{ kind, method, path, name string }{
		{OpRemoved, "GET", "/legacy/export", ""},
		{OpMoved, "POST", "/orders/{id}/refund", ""},
		{ParamRenamed, "GET", "/orders", "per_page"},
		{ParamRequired, "GET", "/orders", "region"},
		{FieldRenamed, "POST", "/orders", "coupon"},
		{FieldRemoved, "POST", "/orders", "qty"},
		{FieldRemoved, "POST", "/orders", "address.zip"},
		{FieldRequired, "POST", "/orders", "currency"},
		{FieldDeprecated, "POST", "/orders", "notes"},
		{EnumRemoved, "POST", "/orders", "shipping_speed"},
		{RespFieldRemoved, "GET", "/orders/{id}", "legacy_status_code"},
	}
	for _, c := range cases {
		if find(cs, c.kind, c.method, c.path, c.name) == nil {
			t.Errorf("missing %s %s %s %s", c.kind, c.method, c.path, c.name)
		}
	}
	// A renamed path parameter is invisible to callers.
	for _, c := range cs.Changes {
		if c.Path == "/orders/{id}" && c.Kind == OpRemoved {
			t.Errorf("path param rename reported as removal")
		}
	}
	if c := find(cs, OpMoved, "POST", "/orders/{id}/refund", ""); c.NewPath != "/orders/{id}/refunds" {
		t.Errorf("moved to %q", c.NewPath)
	}
	if c := find(cs, FieldRenamed, "POST", "/orders", "coupon"); c.NewName != "coupon_code" || !c.SafeRename {
		t.Errorf("rename %+v", c)
	}
	if c := find(cs, FieldDeprecated, "POST", "/orders", "notes"); c.Hint != "comment" || !c.SafeRename {
		t.Errorf("hint %+v", c)
	}
	if c := find(cs, EnumRemoved, "POST", "/orders", "shipping_speed"); len(c.Values) != 1 || c.Values[0] != "overnight" {
		t.Errorf("enum values %+v", c)
	}
	// Removing a nested object reports the object once, not each child.
	n := 0
	for _, c := range cs.Changes {
		if c.Kind == RespFieldRemoved && c.Path == "/orders" && c.Method == "GET" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want one response removal under GET /orders, got %d", n)
	}
}

func TestDeprecationsAndShape(t *testing.T) {
	cs := Deprecations(load(t, "../../testdata/shop-v2.yaml"))
	if len(cs.Changes) != 1 || cs.Changes[0].Name != "notes" {
		t.Fatalf("deprecations: %+v", cs.Changes)
	}
	fs := []oas.Field{
		{Path: "functions", Type: "array"}, {Path: "functions[].name", Type: "string"},
		{Path: "tools", Type: "array"}, {Path: "tools[].type", Type: "string"}, {Path: "tools[].function", Type: "object"},
		{Path: "max_tokens", Type: "integer"}, {Path: "max_completion_tokens", Type: "integer"},
	}
	if sameShape(fs, "functions", "tools") {
		t.Error("functions and tools have different shapes")
	}
	if !sameShape(fs, "max_tokens", "max_completion_tokens") {
		t.Error("integer to integer should be a safe rename")
	}
}

func TestHints(t *testing.T) {
	cases := map[string]string{
		"Deprecated in favor of [`max_completion_tokens`](/docs/x).": "max_completion_tokens",
		"This field is deprecated. Use `tools` instead.":             "tools",
		"Replaced by `page_size`.":                                   "page_size",
		"Use this field to set the limit.":                           "",
	}
	for in, want := range cases {
		if got := hint(in); got != want {
			t.Errorf("hint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNameSimilarity(t *testing.T) {
	if nameSimilarity("max_tokens", "max_output_tokens") < 0.5 {
		t.Error("max_tokens ~ max_output_tokens")
	}
	if nameSimilarity("perPage", "page_size") < 0.5 {
		t.Error("camel and snake words should compare")
	}
	if nameSimilarity("user", "safety_identifier") != 0 {
		t.Error("unrelated names")
	}
}

func TestOpenEnumIsNotBreaking(t *testing.T) {
	old := &oas.API{Ops: []*oas.Operation{{Method: "POST", Path: "/x", Request: []oas.Field{{Path: "model", Name: "model", Type: "string|string", Enum: []string{"a", "b"}}}}}}
	nw := &oas.API{Ops: []*oas.Operation{{Method: "POST", Path: "/x", Request: []oas.Field{{Path: "model", Name: "model", Type: "string|string", Enum: []string{"a"}}}}}}
	if cs := Diff(old, nw); len(cs.Changes) != 0 {
		t.Fatalf("open enum change reported: %+v", cs.Changes)
	}
}
