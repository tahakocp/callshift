#!/usr/bin/env python3
"""Download the corpus files listed in manifest.json by git blob SHA.

The files belong to their authors and are not stored in this repository.
Set GITHUB_TOKEN to avoid the unauthenticated rate limit (60 requests/hour).
"""
import base64, json, os, sys, urllib.request

here = os.path.dirname(os.path.abspath(__file__))
out = os.path.join(here, "_corpus")
os.makedirs(out, exist_ok=True)
token = os.environ.get("GITHUB_TOKEN")
missing = 0
for item in json.load(open(os.path.join(here, "manifest.json"))):
    dest = os.path.join(out, item["repo"].replace("/", "__") + "__" + item["path"].replace("/", "__"))
    if os.path.exists(dest):
        continue
    req = urllib.request.Request(f"https://api.github.com/repos/{item['repo']}/git/blobs/{item['sha']}")
    req.add_header("Accept", "application/vnd.github+json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req) as r:
            blob = json.load(r)
    except Exception as e:  # deleted repos, rate limits
        print(f"skip {item['repo']}/{item['path']}: {e}", file=sys.stderr)
        missing += 1
        continue
    with open(dest, "wb") as f:
        f.write(base64.b64decode(blob["content"]))
print(f"done, {missing} missing")
