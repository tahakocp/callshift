package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("CALLSHIFT_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func run(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "CALLSHIFT_RUN_MAIN=1")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestCLI(t *testing.T) {
	td, _ := filepath.Abs("../../testdata")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.py"), []byte(`import requests
r = requests.get("https://api.shop.example/v1/orders", params={"per_page": 5, "region": "eu"})
`), 0o644)
	v1, v2 := filepath.Join(td, "shop-v1.yaml"), filepath.Join(td, "shop-v2.yaml")

	out, code := run(t, dir, "diff", v1, v2, "-o", "changes.json")
	if code != 0 {
		t.Fatalf("diff exit %d: %s", code, out)
	}
	var cs struct {
		Schema  string `json:"schema"`
		Changes []any  `json:"changes"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "changes.json"))
	if err := json.Unmarshal(b, &cs); err != nil || cs.Schema != "callshift/changes/1" || len(cs.Changes) != 13 {
		t.Fatalf("change set: %v %s", err, b)
	}

	out, code = run(t, dir, "scan", "--changes", "changes.json", ".")
	if code != 1 || !strings.Contains(out, "app.py:2:") || !strings.Contains(out, "per_page") {
		t.Fatalf("scan exit %d:\n%s", code, out)
	}
	out, code = run(t, dir, "scan", "--changes", "changes.json", "--fail-on", "none", ".")
	if code != 0 {
		t.Fatalf("--fail-on none exit %d: %s", code, out)
	}
	out, code = run(t, dir, "scan", "--old", v1, "--new", v2, "--fix", ".")
	if code != 0 && code != 1 {
		t.Fatalf("fix exit %d: %s", code, out)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "app.py"))
	if !strings.Contains(string(b), `"page_size": 5`) {
		t.Fatalf("fix not applied:\n%s\n%s", b, out)
	}
	if _, code = run(t, dir, "scan", "--spec", "missing.yaml", "."); code != 2 {
		t.Fatalf("missing spec should exit 2, got %d", code)
	}
	if out, _ = run(t, dir, "version"); strings.TrimSpace(out) == "" {
		t.Fatal("empty version")
	}
}
