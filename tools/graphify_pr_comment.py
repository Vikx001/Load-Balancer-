#!/usr/bin/env python3
"""Generate a concise Graphify PR comment summarizing nodes related to changed files.

This script is intended to be used in CI after `graphify update` has produced
`graphify-out/graph.json`. It reads a list of changed files and produces a
Markdown file suitable for posting as a PR comment.
"""

from __future__ import annotations

import argparse
import json
import os
import re
from collections import defaultdict
from typing import List


def load_changed(path: str) -> List[str]:
    if not os.path.isfile(path):
        return []
    with open(path, "r", encoding="utf-8") as f:
        return [line.strip() for line in f if line.strip()]


def load_graph(path: str) -> dict:
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def extract_line(loc: str) -> int:
    if not loc:
        return 1
    m = re.search(r"L(\d+)", loc)
    return int(m.group(1)) if m else 1


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="Create Graphify PR summary")
    p.add_argument("--graph", default="graphify-out/graph.json")
    p.add_argument("--changed", default="changed_files.txt")
    p.add_argument("--out", default="graphify_pr_comment.md")
    p.add_argument("--limit", type=int, default=5)
    args = p.parse_args(argv)

    changed = load_changed(args.changed)
    if not changed:
        body = (
            "**Graphify:** no changed files detected or changed file list is empty.\n"
            "If this was expected, ensure the CI step wrote the changed files list."
        )
        with open(args.out, "w", encoding="utf-8") as f:
            f.write(body)
        return 0

    if not os.path.isfile(args.graph):
        with open(args.out, "w", encoding="utf-8") as f:
            f.write("**Graphify:** graph not found (graphify-out/graph.json) — unable to summarize.")
        return 1

    graph = load_graph(args.graph)
    nodes = graph.get("nodes", [])

    # Map changed file -> matching nodes
    file_nodes = defaultdict(list)
    for n in nodes:
        sf = n.get("source_file") or ""
        for cf in changed:
            if not cf:
                continue
            # match by suffix (path fragment) or exact
            if sf.endswith(cf) or cf in sf:
                file_nodes[cf].append(n)

    lines = []
    lines.append("### Graphify PR Summary")
    lines.append("")
    lines.append(f"- Changed files: {len(changed)}")
    lines.append("")

    total_nodes = sum(len(v) for v in file_nodes.values())
    lines.append(f"- Graph nodes associated with changed files: {total_nodes}")
    lines.append("")

    for cf in changed:
        ln = file_nodes.get(cf, [])
        lines.append(f"**{cf}** — {len(ln)} node(s)")
        if not ln:
            lines.append("\n")
            continue
        for n in ln[: args.limit]:
            label = n.get("label") or n.get("id") or "<unknown>"
            sf = n.get("source_file") or cf
            loc = n.get("source_location") or ""
            line_no = extract_line(loc)
            # Link to file at approximate line
            lines.append(f"- `{label}` — [{sf}]({sf}#L{line_no})")
        lines.append("\n")

    body = "\n".join(lines)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(body)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
