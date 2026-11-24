package scan

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/tahakocp/callshift/internal/change"
	"github.com/tahakocp/callshift/internal/oas"
)

func shopSet(t *testing.T) *change.Set {
	t.Helper()
	a, err := oas.Load("../../testdata/shop-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := oas.Load("../../testdata/shop-v2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return change.Diff(a, b)
}

// summary renders findings as "line kind name [-> fix]" for comparison.
func summary(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		s := fmt.Sprintf("%d %s %s", f.Line, f.Kind, f.Name)
		if f.Fix != nil {
			s += " -> " + f.Fix.New
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func expect(t *testing.T, name, src string, want ...string) {
	t.Helper()
	s := New(shopSet(t))
	got := summary(s.Source(name, src, Medium))
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n got:\n  %s\nwant:\n  %s", name, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestTypeScriptFetch(t *testing.T) {
	expect(t, "a.ts", `
const BASE = "https://api.shop.example/v1";
export async function buy(sku: string) {
  const res = await fetch(`+"`${BASE}/orders`"+`, {
    method: "POST",
    body: JSON.stringify({ sku, qty: 2, coupon: "X1", notes: "hi", shipping_speed: "overnight" }),
  });
  const o = await res.json();
  return o.legacy_status_code;
}
`,
		"6 field-deprecated notes -> comment",
		"6 field-removed qty",
		"6 field-renamed coupon -> coupon_code",
		"6 enum-removed shipping_speed",
		"4 field-required currency",
	)
}

func TestPythonRequestsWithBodyVariable(t *testing.T) {
	expect(t, "a.py", `
import requests
API = "https://api.shop.example/v1"

def orders(page):
    params = {"page": page, "per_page": 50}
    return requests.get(f"{API}/orders", params=params).json()

def export():
    return requests.get(API + "/legacy/export")
`,
		"6 param-renamed per_page -> page_size",
		"10 op-removed ",
	)
}

func TestGoNewRequestAndStructTags(t *testing.T) {
	expect(t, "a.go", `package shop

type newOrder struct {
	Sku    string `+"`json:\"sku\"`"+`
	Coupon string `+"`json:\"coupon,omitempty\"`"+`
}

func (c *Client) Refund(id string) error {
	body, err := json.Marshal(newOrder{Sku: "a"})
	if err != nil {
		return err
	}
	req, _ := http.NewRequest("POST", c.base+"/orders/"+id+"/refund", bytes.NewReader(body))
	_ = req
	return nil
}
`,
		"13 op-moved  -> refunds",
	)
}

func TestGoStructLinkedToCall(t *testing.T) {
	expect(t, "b.go", `package shop

const ordersURL = "https://api.shop.example/v1/orders"

type newOrder struct {
	Sku    string `+"`json:\"sku\"`"+`
	Coupon string `+"`json:\"coupon,omitempty\"`"+`
	Currency string `+"`json:\"currency\"`"+`
}

func (c *Client) Buy() error {
	body, _ := json.Marshal(newOrder{Sku: "a"})
	req, _ := http.NewRequest(http.MethodPost, ordersURL, bytes.NewReader(body))
	_ = req
	return nil
}
`,
		"7 field-renamed coupon -> coupon_code",
	)
}

func TestShellCurlAndCIYaml(t *testing.T) {
	expect(t, "deploy.sh", `#!/bin/sh
curl -s "https://api.shop.example/v1/orders?page=1&per_page=20&region=eu" \
  -H "Authorization: Bearer $TOKEN"
curl -X POST https://api.shop.example/v1/orders -d '{"sku": "a", "qty": 1, "currency": "EUR"}'
`,
		"2 param-renamed per_page -> page_size",
		"4 field-removed qty",
	)
	expect(t, "ci.yml", `jobs:
  x:
    steps:
      - run: curl https://api.shop.example/v1/legacy/export -o out.csv
`,
		"4 op-removed ",
	)
}

func TestRubyJavaPHP(t *testing.T) {
	expect(t, "a.rb", `
uri = URI("https://api.shop.example/v1/orders")
req = Net::HTTP::Post.new(uri)
req.body = { sku: "a", coupon: "Z", currency: "EUR" }.to_json
`, "4 field-renamed coupon") // found through the surrounding block: reported, not rewritten
	expect(t, "A.java", `
class A {
  void buy() throws Exception {
    URL url = new URL("https://api.shop.example/v1/orders");
    JSONObject body = new JSONObject();
    body.put("sku", "a");
    body.put("qty", 3);
    body.put("currency", "EUR");
  }
}
`, "7 field-removed qty")
	expect(t, "a.php", `<?php
$data = ["sku" => "a", "notes" => "x", "currency" => "EUR"];
$ch = curl_init("https://api.shop.example/v1/orders");
curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($data));
`, "2 field-deprecated notes -> comment")
}

func TestOctokitStyleRoute(t *testing.T) {
	expect(t, "r.js", `
await client.request("GET /orders", { page: 1, per_page: 10, region: "eu" })
`, "2 param-renamed per_page -> page_size")
}

func TestOtherHostIgnored(t *testing.T) {
	expect(t, "o.js", `
await fetch("https://api.other.example/v1/orders", { method: "POST", body: JSON.stringify({ qty: 1 }) })
`)
}

func TestUnrelatedPathsIgnored(t *testing.T) {
	expect(t, "u.ts", `
import x from "./orders/index";
const css = "/static/orders.css";
const s = "no/orders here";
`)
}

func TestCommentsIgnored(t *testing.T) {
	expect(t, "c.js", `
// fetch("https://api.shop.example/v1/legacy/export")
/* curl https://api.shop.example/v1/legacy/export */
`)
}

func TestConfigObjectKeysAreWeak(t *testing.T) {
	s := New(shopSet(t))
	fs := s.Source("cfg.js", `
const settings = {
  url: "https://api.shop.example/v1/orders",
  qty: 5,
};
`, Low)
	if len(fs) != 2 {
		t.Fatalf("want 2 low findings, got %+v", fs)
	}
	for _, f := range fs {
		if f.Level != Low || f.Fix != nil {
			t.Errorf("findings from a settings object should be low confidence with no fix: %+v", f)
		}
	}
}

func TestShorthandHasNoFix(t *testing.T) {
	s := New(shopSet(t))
	fs := s.Source("sh.js", `
const coupon = "A";
fetch("https://api.shop.example/v1/orders", { method: "POST", body: JSON.stringify({ sku: "a", coupon, currency: "EUR" }) });
`, Medium)
	if len(fs) != 1 || fs[0].Kind != change.FieldRenamed || fs[0].Fix != nil {
		t.Fatalf("shorthand property must be reported without an automatic rename: %+v", fs)
	}
}

func TestEditsAreExact(t *testing.T) {
	src := `fetch("https://api.shop.example/v1/orders", { method: "POST", body: JSON.stringify({ "sku": "a", "coupon": "A", "currency": "EUR" }) })`
	s := New(shopSet(t))
	fs := s.Source("e.js", src, Medium)
	if len(fs) != 1 || fs[0].Fix == nil {
		t.Fatalf("want one fixable finding, got %+v", fs)
	}
	e := fs[0].Fix
	if src[e.Off:e.End] != "coupon" {
		t.Fatalf("edit points at %q", src[e.Off:e.End])
	}
}
