package report

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tahakocp/callshift/internal/change"
	"github.com/tahakocp/callshift/internal/oas"
	"github.com/tahakocp/callshift/internal/scan"
)

const src = `const BASE = "https://api.shop.example/v1";
await fetch(BASE + "/orders", {
  method: "POST",
  body: JSON.stringify({ "sku": "a", "coupon": "A", "currency": "EUR", "notes": "n" }),
});
await fetch(BASE + "/legacy/export");
`

func findings(t *testing.T, dir string) ([]scan.Finding, *change.Set, string) {
	t.Helper()
	a, err := oas.Load("../../testdata/shop-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := oas.Load("../../testdata/shop-v2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cs := change.Diff(a, b)
	file := filepath.Join(dir, "client.js")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, _, err := scan.New(cs).Paths([]string{file}, scan.Options{MinConfidence: scan.Medium})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 3 {
		t.Fatalf("want 3 findings, got %d: %+v", len(fs), fs)
	}
	return fs, cs, file
}

func TestSARIF(t *testing.T) {
	fs, _, _ := findings(t, t.TempDir())
	var buf bytes.Buffer
	if err := SARIF(&buf, fs, "test"); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
				Level  string `json:"level"`
				Fixes  []any  `json:"fixes"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != "2.1.0" || len(doc.Runs) != 1 || len(doc.Runs[0].Results) != 3 {
		t.Fatalf("unexpected SARIF: %s", buf.String())
	}
	fixes := 0
	for _, r := range doc.Runs[0].Results {
		fixes += len(r.Fixes)
		if r.RuleID == "" || r.Level == "" {
			t.Errorf("result without rule or level: %+v", r)
		}
	}
	if fixes != 2 {
		t.Errorf("want 2 fixes in SARIF, got %d", fixes)
	}
}

func TestDiffAppliesWithGit(t *testing.T) {
	dir := t.TempDir()
	fs, _, _ := findings(t, dir)
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	var buf bytes.Buffer
	if err := Diff(&buf, fs); err != nil {
		t.Fatal(err)
	}
	d := buf.String()
	if !strings.Contains(d, `+  body: JSON.stringify({ "sku": "a", "coupon_code": "A", "currency": "EUR", "comment": "n" }),`) {
		t.Fatalf("diff:\n%s", d)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// The patch must apply cleanly to the original file.
	patch := d
	if !strings.HasPrefix(d, "--- a/client.js\n") {
		t.Fatalf("paths should be relative:\n%s", d)
	}
	cmd := exec.Command("git", "apply", "--check", "-")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s\n%s", err, out, patch)
	}
}

func TestApply(t *testing.T) {
	fs, _, file := findings(t, t.TempDir())
	n, err := Apply(fs)
	if err != nil || n != 2 {
		t.Fatalf("applied %d, %v", n, err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), `"coupon_code": "A"`) || strings.Contains(string(b), `"notes"`) {
		t.Fatalf("file after fix:\n%s", b)
	}
	// Running again is a no-op: the old text is gone.
	if n, _ := Apply(fs); n != 0 {
		t.Fatalf("second apply changed %d places", n)
	}
}

func TestMarkdownAndText(t *testing.T) {
	fs, cs, _ := findings(t, t.TempDir())
	var md, txt bytes.Buffer
	Markdown(&md, fs, scan.Stats{}, cs)
	Text(&txt, fs, scan.Stats{}, false)
	if !strings.Contains(md.String(), "| ") || !strings.Contains(md.String(), "/legacy/export") {
		t.Fatalf("markdown:\n%s", md.String())
	}
	if !strings.Contains(txt.String(), "3 finding(s), 2 with a safe fix") {
		t.Fatalf("text:\n%s", txt.String())
	}
}
