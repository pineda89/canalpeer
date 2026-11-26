#!/usr/bin/env python3
"""Writes core/mobile/descriptors.snapshot.json.gz from canalpeer.com, so each
build ships every channel the site knows and plays even when the site is
blocked. The file is ignored by git: the repo only carries the empty
descriptors.json.gz. On failure the previous copy stays and the build goes on."""
import gzip, json, sys, urllib.request

OUT = "core/mobile/descriptors.snapshot.json.gz"
items, after = [], 0
try:
    while True:
        with urllib.request.urlopen(f"https://canalpeer.com/api/descriptors?after={after}", timeout=60) as r:
            page = json.load(r)
        if not page["items"]:
            break
        items += page["items"]
        after = page["next"]
except Exception as e:
    print(f"snapshot: kept the previous copy: {e}", file=sys.stderr)
    sys.exit(0)
# mtime=0: the same channels give the same bytes.
data = json.dumps({"items": items, "next": after}, separators=(",", ":")).encode()
with open(OUT, "wb") as f:
    f.write(gzip.compress(data, compresslevel=9, mtime=0))
print(f"snapshot: {len(items)} channels up to {after}")
