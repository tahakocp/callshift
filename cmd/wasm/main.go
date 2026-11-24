//go:build js && wasm

// Command wasm exposes callshift to the browser demo.
package main

import (
	"bytes"
	"encoding/json"
	"syscall/js"

	"github.com/tahakocp/callshift/internal/change"
	"github.com/tahakocp/callshift/internal/oas"
	"github.com/tahakocp/callshift/internal/report"
	"github.com/tahakocp/callshift/internal/scan"
)

func result(v any, err error) any {
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// callshiftDiff(oldSpec, newSpec) -> change set JSON
func diff(_ js.Value, a []js.Value) any {
	old, err := oas.Parse([]byte(a[0].String()))
	if err != nil {
		return result(nil, err)
	}
	nw, err := oas.Parse([]byte(a[1].String()))
	if err != nil {
		return result(nil, err)
	}
	return result(change.Diff(old, nw), nil)
}

// callshiftScan(changeSetJSON, fileName, source, minConfidence) -> {findings, diff}
func scanSource(_ js.Value, a []js.Value) any {
	var cs change.Set
	if err := json.Unmarshal([]byte(a[0].String()), &cs); err != nil {
		return result(nil, err)
	}
	lvl, _ := scan.ParseConfidence(a[3].String())
	name, src := a[1].String(), a[2].String()
	fs := scan.New(&cs).Source(name, src, lvl)
	var d bytes.Buffer
	report.DiffSource(&d, name, src, fs)
	if fs == nil {
		fs = []scan.Finding{}
	}
	return result(map[string]any{"findings": fs, "diff": d.String()}, nil)
}

func main() {
	js.Global().Set("callshiftDiff", js.FuncOf(diff))
	js.Global().Set("callshiftScan", js.FuncOf(scanSource))
	select {}
}
