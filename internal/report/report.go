// Package report renders findings as text, JSON, Markdown, SARIF or a
// unified diff of the safe fixes.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tahakocp/callshift/internal/change"
	"github.com/tahakocp/callshift/internal/scan"
)

// Text prints one line per finding, grep style.
func Text(w io.Writer, fs []scan.Finding, st scan.Stats, color bool) {
	for _, f := range fs {
		sev := f.Severity
		if color {
			switch f.Severity {
			case change.Breaking:
				sev = "\x1b[31m" + sev + "\x1b[0m"
			default:
				sev = "\x1b[33m" + sev + "\x1b[0m"
			}
		}
		guess := ""
		if f.MethodGuessed {
			guess = "?"
		}
		fmt.Fprintf(w, "%s:%d:%d: %s [%s, %s confidence] %s%s %s\n", f.File, f.Line, f.Col, f.Message, sev, f.Confidence, f.Method, guess, f.Path)
		if f.Snippet != "" {
			fmt.Fprintf(w, "    %s\n", f.Snippet)
		}
		if f.Fix != nil {
			fmt.Fprintf(w, "    fix: %s -> %s\n", f.Fix.Old, f.Fix.New)
		}
	}
	fixes := 0
	for _, f := range fs {
		if f.Fix != nil {
			fixes++
		}
	}
	fmt.Fprintf(w, "%d finding(s), %d with a safe fix; %d file(s), %.1f MB in %s\n", len(fs), fixes, st.Files, float64(st.Bytes)/1e6, duration(st.Duration))
}

func duration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

// JSON writes findings and stats.
func JSON(w io.Writer, fs []scan.Finding, st scan.Stats, cs *change.Set) error {
	if fs == nil {
		fs = []scan.Finding{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"api": cs.API, "from": cs.From, "to": cs.To, "stats": st, "findings": fs})
}

// Markdown writes a table suitable for a pull request comment.
func Markdown(w io.Writer, fs []scan.Finding, st scan.Stats, cs *change.Set) {
	title := cs.API
	if cs.From != "" || cs.To != "" {
		title += " " + strings.TrimSpace(cs.From+" → "+cs.To)
	}
	fmt.Fprintf(w, "### callshift: %s\n\n", strings.TrimSpace(title))
	if len(fs) == 0 {
		fmt.Fprintf(w, "No affected call sites in %d files.\n", st.Files)
		return
	}
	fmt.Fprintf(w, "| Where | Change | Severity | Confidence | Fix |\n|---|---|---|---|---|\n")
	for _, f := range fs {
		fix := ""
		if f.Fix != nil {
			fix = fmt.Sprintf("`%s` → `%s`", f.Fix.Old, f.Fix.New)
		}
		fmt.Fprintf(w, "| `%s:%d` | %s | %s | %s | %s |\n", f.File, f.Line, strings.ReplaceAll(f.Message, "|", "\\|"), f.Severity, f.Confidence, fix)
	}
	fmt.Fprintf(w, "\n%d finding(s) in %d files scanned.\n", len(fs), st.Files)
}

// SARIF writes SARIF 2.1.0 for GitHub code scanning and other viewers.
func SARIF(w io.Writer, fs []scan.Finding, version string) error {
	rulesSeen := map[string]bool{}
	var rules []map[string]any
	var results []map[string]any
	for _, f := range fs {
		if !rulesSeen[f.Kind] {
			rulesSeen[f.Kind] = true
			rules = append(rules, map[string]any{
				"id":               f.Kind,
				"shortDescription": map[string]string{"text": ruleText[f.Kind]},
				"helpUri":          "https://github.com/tahakocp/callshift#what-it-reports",
			})
		}
		level := "warning"
		if f.Severity == change.Breaking && f.Level >= scan.Medium {
			level = "error"
		}
		if f.Level == scan.Low {
			level = "note"
		}
		res := map[string]any{
			"ruleId":  f.Kind,
			"level":   level,
			"message": map[string]string{"text": f.Message + " (" + f.Method + " " + f.Path + ")"},
			"locations": []map[string]any{{"physicalLocation": map[string]any{
				"artifactLocation": map[string]string{"uri": strings.TrimPrefix(f.File, "./")},
				"region":           map[string]int{"startLine": f.Line, "startColumn": f.Col},
			}}},
			"properties": map[string]string{"confidence": f.Confidence},
		}
		if f.Fix != nil {
			res["fixes"] = []map[string]any{{
				"description": map[string]string{"text": "rename " + f.Fix.Old + " to " + f.Fix.New},
				"artifactChanges": []map[string]any{{
					"artifactLocation": map[string]string{"uri": strings.TrimPrefix(f.File, "./")},
					"replacements": []map[string]any{{
						"deletedRegion":   map[string]int{"startLine": f.Line, "startColumn": f.Col, "endColumn": f.Col + len(f.Fix.Old)},
						"insertedContent": map[string]string{"text": f.Fix.New},
					}},
				}},
			}}
		}
		results = append(results, res)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i]["id"].(string) < rules[j]["id"].(string) })
	if results == nil {
		results = []map[string]any{}
	}
	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []map[string]any{{
			"tool": map[string]any{"driver": map[string]any{
				"name": "callshift", "version": version, "informationUri": "https://github.com/tahakocp/callshift", "rules": rules,
			}},
			"results": results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

var ruleText = map[string]string{
	change.OpRemoved:        "Call to an operation that the API removed",
	change.OpMoved:          "Call to an operation that moved to a new path",
	change.OpDeprecated:     "Call to a deprecated operation",
	change.ParamRemoved:     "Parameter that the API removed",
	change.ParamRenamed:     "Parameter that the API renamed",
	change.ParamRequired:    "Call misses a parameter the API now requires",
	change.ParamDeprecated:  "Deprecated parameter",
	change.FieldRemoved:     "Request field that the API removed",
	change.FieldRenamed:     "Request field that the API renamed",
	change.FieldRequired:    "Call may miss a request field the API now requires",
	change.FieldDeprecated:  "Deprecated request field",
	change.FieldType:        "Request field whose type changed",
	change.EnumRemoved:      "Value no longer allowed for a request field",
	change.RespFieldRemoved: "Possible read of a response field that the API removed",
	change.RespFieldRenamed: "Possible read of a response field that the API renamed",
}

// Apply writes all safe fixes to disk and returns how many were applied.
func Apply(fs []scan.Finding) (int, error) {
	byFile := groupFixes(fs)
	n := 0
	for file, edits := range byFile {
		b, err := os.ReadFile(file)
		if err != nil {
			return n, err
		}
		out, k := applyEdits(string(b), edits)
		if k == 0 {
			continue
		}
		if err := os.WriteFile(file, []byte(out), 0o644); err != nil {
			return n, err
		}
		n += k
	}
	return n, nil
}

// Diff writes a unified diff of all safe fixes without touching files.
func Diff(w io.Writer, fs []scan.Finding) error {
	byFile := groupFixes(fs)
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		before := string(b)
		after, k := applyEdits(before, byFile[file])
		if k == 0 {
			continue
		}
		unified(w, displayPath(file), before, after)
	}
	return nil
}

// DiffSource is Diff for one in-memory file.
func DiffSource(w io.Writer, name, src string, fs []scan.Finding) {
	if after, k := applyEdits(src, groupFixes(fs)[name]); k > 0 {
		unified(w, name, src, after)
	}
}

// displayPath makes a path relative to the working directory when it can,
// so the diff applies with "git apply" or "patch -p1".
func displayPath(file string) string {
	if filepath.IsAbs(file) {
		if wd, err := os.Getwd(); err == nil {
			f := file
			if e, err := filepath.EvalSymlinks(f); err == nil {
				f = e
			}
			if e, err := filepath.EvalSymlinks(wd); err == nil {
				wd = e
			}
			if r, err := filepath.Rel(wd, f); err == nil && !strings.HasPrefix(r, "..") {
				file = r
			}
		}
	}
	return filepath.ToSlash(strings.TrimPrefix(file, "./"))
}

func groupFixes(fs []scan.Finding) map[string][]scan.Edit {
	by := map[string][]scan.Edit{}
	for _, f := range fs {
		if f.Fix != nil {
			by[f.File] = append(by[f.File], *f.Fix)
		}
	}
	return by
}

// applyEdits applies non-overlapping edits from the end of the file.
func applyEdits(src string, edits []scan.Edit) (string, int) {
	sort.Slice(edits, func(i, j int) bool { return edits[i].Off > edits[j].Off })
	n := 0
	last := len(src) + 1
	for _, e := range edits {
		if e.End > last || e.Off < 0 || e.End > len(src) || src[e.Off:e.End] != e.Old ||
			(e.Off > 0 && word(src[e.Off-1])) || (e.End < len(src) && word(src[e.End])) {
			continue
		}
		src = src[:e.Off] + e.New + src[e.End:]
		last = e.Off
		n++
	}
	return src, n
}

func word(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// unified prints a diff for line-preserving edits (renames never add or
// remove lines), with three lines of context.
func unified(w io.Writer, name, a, b string) {
	al := strings.Split(strings.TrimSuffix(a, "\n"), "\n")
	bl := strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	if len(al) != len(bl) {
		return
	}
	var changed []int
	for i := range al {
		if al[i] != bl[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return
	}
	fmt.Fprintf(w, "--- a/%s\n+++ b/%s\n", name, name)
	for i := 0; i < len(changed); {
		start := max(changed[i]-3, 0)
		end := min(changed[i]+3, len(al)-1)
		j := i + 1
		for j < len(changed) && changed[j]-3 <= end+1 {
			end = min(changed[j]+3, len(al)-1)
			j++
		}
		n := end - start + 1
		fmt.Fprintf(w, "@@ -%d,%d +%d,%d @@\n", start+1, n, start+1, n)
		for k := start; k <= end; k++ {
			if al[k] != bl[k] {
				fmt.Fprintf(w, "-%s\n+%s\n", al[k], bl[k])
			} else {
				fmt.Fprintf(w, " %s\n", al[k])
			}
		}
		i = j
	}
}

// Changes prints a change set for people.
func Changes(w io.Writer, cs *change.Set) {
	fmt.Fprintf(w, "%s %s\n", cs.API, strings.TrimSpace(cs.From+" -> "+cs.To))
	count := map[string]int{}
	for _, c := range cs.Changes {
		count[c.Severity]++
		line := fmt.Sprintf("  %-22s %-6s %s", c.Kind, c.Method, c.Path)
		if c.NewPath != "" {
			line += " -> " + c.NewPath
		}
		if c.Name != "" {
			line += "  " + c.In + " " + c.Name
		}
		if c.NewName != "" {
			line += " -> " + c.NewName
		}
		if c.Hint != "" {
			line += "  (use " + c.Hint + ")"
		}
		if len(c.Values) > 0 {
			v := c.Values
			if len(v) > 6 {
				v = append(append([]string{}, v[:6]...), fmt.Sprintf("+%d more", len(c.Values)-6))
			}
			line += "  values: " + strings.Join(v, ", ")
		}
		if c.Basis != "" && c.Kind != change.OpMoved {
			line += "  [" + c.Basis + "]"
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "%d breaking, %d deprecated; %d operations known\n", count[change.Breaking], count[change.Deprecated], len(cs.Ops))
}
