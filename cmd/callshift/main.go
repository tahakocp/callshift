// Command callshift finds the call sites in your code that an API change
// breaks, and rewrites the ones that are a plain rename.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/tahakocp/callshift/internal/change"
	"github.com/tahakocp/callshift/internal/oas"
	"github.com/tahakocp/callshift/internal/report"
	"github.com/tahakocp/callshift/internal/scan"
)

var version = "dev"

const usage = `callshift: find the code an API change breaks.

Usage:
  callshift diff OLD_SPEC NEW_SPEC [-o changes.json]
  callshift deprecations SPEC [-o changes.json]
  callshift scan (--old OLD --new NEW | --spec SPEC | --changes FILE) [flags] [PATH...]
  callshift version

Specs are OpenAPI 3.x or Swagger 2.0, JSON or YAML.
A change set (changes.json) is what diff writes; an API provider can publish
one per release so clients scan without the full specs.

Scan flags:
  --format text|json|md|sarif   output format (default text)
  --min-confidence low|medium|high  hide weaker findings (default medium)
  --fail-on breaking|deprecated|none  exit 1 when findings reach this severity (default breaking)
  --diff                        print a unified diff of the safe renames
  --fix                         apply the safe renames in place
  --exclude GLOB                skip paths (repeatable)
  -o FILE                       write output to FILE
`

func init() {
	// "go install ...@v0.1.0" builds without ldflags; take the module version.
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	code := 0
	switch os.Args[1] {
	case "diff":
		err = cmdDiff(os.Args[2:])
	case "deprecations":
		err = cmdDeprecations(os.Args[2:])
	case "scan":
		code, err = cmdScan(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("callshift", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "callshift:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

// parseInterleaved lets flags come after positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func output(path string) (io.Writer, func() error, error) {
	if path == "" || path == "-" {
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	out := fs.String("o", "", "write the change set as JSON to this file")
	format := fs.String("format", "", "text or json (default: json with -o, text otherwise)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("diff needs OLD_SPEC and NEW_SPEC")
	}
	old, err := oas.Load(pos[0])
	if err != nil {
		return fmt.Errorf("%s: %w", pos[0], err)
	}
	nw, err := oas.Load(pos[1])
	if err != nil {
		return fmt.Errorf("%s: %w", pos[1], err)
	}
	return writeSet(change.Diff(old, nw), *out, *format)
}

func cmdDeprecations(args []string) error {
	fs := flag.NewFlagSet("deprecations", flag.ContinueOnError)
	out := fs.String("o", "", "write the change set as JSON to this file")
	format := fs.String("format", "", "text or json")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("deprecations needs one SPEC")
	}
	a, err := oas.Load(pos[0])
	if err != nil {
		return fmt.Errorf("%s: %w", pos[0], err)
	}
	return writeSet(change.Deprecations(a), *out, *format)
}

func writeSet(cs *change.Set, out, format string) error {
	if format == "" {
		format = "text"
		if out != "" {
			format = "json"
		}
	}
	w, closeFn, err := output(out)
	if err != nil {
		return err
	}
	defer closeFn()
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(cs)
	}
	report.Changes(w, cs)
	return nil
}

func cmdScan(args []string) (int, error) {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	oldSpec := fs.String("old", "", "spec your code was written against")
	newSpec := fs.String("new", "", "spec of the version you are moving to")
	spec := fs.String("spec", "", "single spec: report use of anything it marks deprecated")
	changes := fs.String("changes", "", "change set JSON written by callshift diff")
	format := fs.String("format", "text", "text, json, md or sarif")
	minConf := fs.String("min-confidence", "medium", "low, medium or high")
	failOn := fs.String("fail-on", "breaking", "breaking, deprecated or none")
	doFix := fs.Bool("fix", false, "apply safe renames in place")
	doDiff := fs.Bool("diff", false, "print safe renames as a unified diff")
	out := fs.String("o", "", "write output to this file")
	var excludes multi
	fs.Var(&excludes, "exclude", "glob to skip (repeatable)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2, err
	}
	if len(pos) == 0 {
		pos = []string{"."}
	}
	switch *failOn {
	case "breaking", "deprecated", "none":
	default:
		return 2, fmt.Errorf("bad --fail-on %q (breaking, deprecated or none)", *failOn)
	}
	lvl, ok := scan.ParseConfidence(*minConf)
	if !ok {
		return 2, fmt.Errorf("bad --min-confidence %q", *minConf)
	}
	cs, err := loadSet(*oldSpec, *newSpec, *spec, *changes)
	if err != nil {
		return 2, err
	}
	s := scan.New(cs)
	findings, st, err := s.Paths(pos, scan.Options{MinConfidence: lvl, Exclude: excludes})
	if err != nil {
		return 2, err
	}
	w, closeFn, err := output(*out)
	if err != nil {
		return 2, err
	}
	defer closeFn()
	switch {
	case *doDiff:
		err = report.Diff(w, findings)
	case *format == "json":
		err = report.JSON(w, findings, st, cs)
	case *format == "md" || *format == "markdown":
		report.Markdown(w, findings, st, cs)
	case *format == "sarif":
		err = report.SARIF(w, findings, version)
	case *format == "text":
		report.Text(w, findings, st, *out == "" && isTerminal())
	default:
		return 2, fmt.Errorf("unknown format %q", *format)
	}
	if err != nil {
		return 2, err
	}
	if *doFix {
		n, err := report.Apply(findings)
		if err != nil {
			return 2, err
		}
		fmt.Fprintf(os.Stderr, "applied %d fix(es)\n", n)
	}
	return exitCode(findings, *failOn), nil
}

func loadSet(oldSpec, newSpec, spec, changes string) (*change.Set, error) {
	switch {
	case changes != "":
		b, err := os.ReadFile(changes)
		if err != nil {
			return nil, err
		}
		var cs change.Set
		if err := json.Unmarshal(b, &cs); err != nil {
			return nil, fmt.Errorf("%s: %w", changes, err)
		}
		if cs.Schema != change.Schema {
			return nil, fmt.Errorf("%s: schema %q, want %q", changes, cs.Schema, change.Schema)
		}
		return &cs, nil
	case oldSpec != "" && newSpec != "":
		a, err := oas.Load(oldSpec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", oldSpec, err)
		}
		b, err := oas.Load(newSpec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", newSpec, err)
		}
		return change.Diff(a, b), nil
	case spec != "":
		a, err := oas.Load(spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec, err)
		}
		return change.Deprecations(a), nil
	}
	return nil, errors.New("scan needs --old and --new, --spec, or --changes")
}

func exitCode(fs []scan.Finding, failOn string) int {
	for _, f := range fs {
		switch failOn {
		case "none":
			return 0
		case "deprecated":
			return 1
		default:
			if f.Severity == change.Breaking {
				return 1
			}
		}
	}
	return 0
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
