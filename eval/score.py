#!/usr/bin/env python3
"""Score callshift and a regex baseline against labels.json.

usage: python3 eval/score.py [path/to/callshift]
Run eval/fetch.py first.
"""
import collections, json, os, re, subprocess, sys

here = os.path.dirname(os.path.abspath(__file__))
binary = sys.argv[1] if len(sys.argv) > 1 else "callshift"
corpus = os.path.join(here, "_corpus")
key = lambda repo, path: repo.replace("/", "__") + "__" + path.replace("/", "__")

labels = {(key(l["repo"], l["path"]), l["line"]): l["true"] for l in json.load(open(os.path.join(here, "labels.json")))}
truth = {k for k, v in labels.items() if v}
present = set(os.listdir(corpus))
truth = {k for k in truth if k[0] in present}

def tool(level):
    r = subprocess.run([binary, "scan", "--changes", os.path.join(here, "openai-deprecations.json"),
                        "--min-confidence", level, "--format", "json", corpus],
                       capture_output=True, text=True)
    if r.returncode == 2:
        sys.exit(r.stderr)
    d = json.loads(r.stdout)
    hits = {(os.path.basename(f["file"]), f["line"]) for f in d["findings"] if f["name"] == "max_tokens"}
    return hits, d["stats"]

def baseline():
    rx = re.compile(r'''["']?\bmax_tokens\b["']?\s*(:|=>|=(?!=)|,|\bto\b)''')
    hits = set()
    for fn in present:
        for i, line in enumerate(open(os.path.join(corpus, fn), errors="replace"), 1):
            if rx.search(line):
                hits.add((fn, i))
    return hits

def show(name, hits):
    tp = len(hits & truth)
    unlabeled = [h for h in hits if h not in labels]
    p = tp / len(hits) if hits else 0
    print(f"{name:22} flagged {len(hits):4}  correct {tp:3}  precision {p:6.1%}  recall {tp}/{len(truth)} = {tp/len(truth):.1%}"
          + (f"  unlabeled {len(unlabeled)}" if unlabeled else ""))
    return hits

print(f"files {len(present)}, true call-site lines {len(truth)}")
for level in ("high", "medium", "low"):
    h, stats = tool(level)
    show("callshift --min " + level, h)
show("regex baseline", baseline())
print(f"scan time {stats['duration_ns']/1e6:.0f} ms for {stats['bytes']/1e6:.2f} MB")
missed = sorted(truth - tool("medium")[0])
if missed:
    print("missed at medium:")
    for f, l in missed:
        print(f"  {f}:{l}")
