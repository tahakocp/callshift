package oas

import "testing"

func TestLoadShop(t *testing.T) {
	a, err := Load("../../testdata/shop-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "Shop API" || len(a.Ops) != 5 {
		t.Fatalf("title %q ops %d", a.Title, len(a.Ops))
	}
	if len(a.Hosts) != 1 || a.Hosts[0] != "api.shop.example" || a.BasePaths[0] != "/v1" {
		t.Fatalf("servers: %v %v", a.Hosts, a.BasePaths)
	}
	o := a.Op("POST", "/orders")
	got := map[string]Field{}
	for _, f := range o.Request {
		got[f.Path] = f
	}
	if !got["sku"].Required || got["qty"].Required {
		t.Fatalf("required flags wrong: %+v", got)
	}
	if _, ok := got["address.zip"]; !ok {
		t.Fatalf("nested field missing: %v", got)
	}
	if e := got["shipping_speed"].Enum; len(e) != 3 {
		t.Fatalf("enum %v", e)
	}
	r := a.Op("GET", "/orders")
	found := false
	for _, f := range r.Response {
		if f.Path == "orders[].legacy_status_code" {
			found = true
		}
	}
	if !found {
		t.Fatalf("array item fields missing: %+v", r.Response)
	}
}

func TestSwagger2AndJSON(t *testing.T) {
	src := `{"swagger":"2.0","info":{"title":"Old","version":"1"},"host":"api.old.example","basePath":"/api",
	"paths":{"/things":{"post":{"parameters":[{"name":"body","in":"body","schema":{"$ref":"#/definitions/T"}},
	{"name":"dry_run","in":"query","type":"boolean"}],"responses":{"200":{"description":"ok","schema":{"$ref":"#/definitions/T"}}}}}},
	"definitions":{"T":{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"self":{"$ref":"#/definitions/T"}}}}}`
	a, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hosts[0] != "api.old.example" || a.BasePaths[0] != "/api" {
		t.Fatalf("host/base: %v %v", a.Hosts, a.BasePaths)
	}
	o := a.Op("POST", "/things")
	if len(o.Params) != 1 || o.Params[0].Name != "dry_run" || o.Params[0].Type != "boolean" {
		t.Fatalf("params %+v", o.Params)
	}
	if len(o.Request) == 0 || o.Request[0].Path != "name" || !o.Request[0].Required {
		t.Fatalf("body %+v", o.Request)
	}
	// The recursive "self" reference must not loop forever and is cut
	// at the cycle.
	for _, f := range o.Request {
		if len(f.Path) > 40 {
			t.Fatalf("recursion not cut: %s", f.Path)
		}
	}
}

func TestVariantRequiredIsNotRequired(t *testing.T) {
	src := `openapi: 3.1.0
info: {title: V, version: "1"}
paths:
  /m:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                content:
                  oneOf:
                    - type: string
                    - type: object
                      required: [kind]
                      properties:
                        kind: {type: string}
          multipart/form-data:
            schema:
              type: object
              required: [file]
              properties:
                file: {type: string}
      responses: {"200": {description: ok}}
`
	a, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range a.Op("POST", "/m").Request {
		if f.Required {
			t.Errorf("%s should not be required (variant or single media type)", f.Path)
		}
	}
}

func TestRejectsNonSpec(t *testing.T) {
	if _, err := Parse([]byte("hello: world")); err == nil {
		t.Fatal("expected an error")
	}
}
