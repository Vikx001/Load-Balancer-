#!/usr/bin/env python3
"""tools/graphify_query.py

Convenience wrapper to run Graphify queries from the project root.

Behavior:
- If a Graphify CLI is available (prefer `.venv-graphify/bin/graphify`, then PATH), it will run the CLI command and stream results.
- Otherwise it falls back to a lightweight JSON search over `graphify-out/graph.json` and prints a concise list of matching nodes with file/line snippets.

Usage examples:
  python tools/graphify_query.py query "circuit breaker" --limit 5
  python tools/graphify_query.py path "CircuitBreakerManager" "lb_policy.bpf.c"
  python tools/graphify_query.py explain "KAN inference"

This script is intentionally dependency-free (stdlib only) so contributors can run it without installing extra packages.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
from typing import List, Optional


def find_graphify_cli() -> Optional[str]:
    venv_path = os.path.join(".venv-graphify", "bin", "graphify")
    if os.path.isfile(venv_path) and os.access(venv_path, os.X_OK):
        return venv_path
    cli = shutil.which("graphify")
    return cli


def run_graphify_cli(args: argparse.Namespace) -> int:
    cli = find_graphify_cli()
    if not cli:
        return 2
    cmd = [cli]
    if args.command == "query":
        cmd += ["query", args.query]
        if args.budget:
            cmd += ["--budget", str(args.budget)]
    elif args.command == "path":
        cmd += ["path", args.a, args.b]
    elif args.command == "explain":
        cmd += ["explain", args.query]
    else:
        print("Unsupported subcommand for CLI path", file=sys.stderr)
        return 3

    if args.graph:
        cmd += ["--graph", args.graph]

    try:
        proc = subprocess.run(cmd, check=False, text=True)
        return proc.returncode
    except Exception as e:
        print("Graphify CLI invocation failed:", e, file=sys.stderr)
        return 4


def load_graph_json(path: str) -> dict:
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def score_node(node: dict, tokens: List[str]) -> int:
    label = (node.get("label") or "").lower()
    source = (node.get("source_file") or "").lower()
    nid = (node.get("id") or "").lower()
    score = 0
    for t in tokens:
        if t in label:
            score += 4 * label.count(t)
        if t in source:
            score += 2 * source.count(t)
        if t in nid:
            score += 1 * nid.count(t)
    return score


def pretty_snippet(repo_root: str, source_file: str, loc: str, tokens: List[str]) -> str:
    path = os.path.join(repo_root, source_file)
    if not os.path.isfile(path):
        return f"(file not found: {source_file})"
    # parse L<number> simple format
    m = re.search(r"L(\d+)", loc or "")
    line_no = int(m.group(1)) if m else 1
    try:
        with open(path, "r", encoding="utf-8", errors="ignore") as f:
            lines = f.readlines()
    except Exception:
        return f"(could not read: {source_file})"
    start = max(0, line_no - 4)
    end = min(len(lines), line_no + 3)
    out = []
    for i in range(start, end):
        prefix = ">" if (i + 1) == line_no else " "
        text = lines[i].rstrip("\n")
        # highlight tokens (simple)
        for t in tokens:
            if t and t in text.lower():
                text = re.sub("(?i)(" + re.escape(t) + ")", r"\x1b[1;31m\1\x1b[0m", text)
        out.append(f"{prefix} {i+1:4d}: {text}")
    return "\n".join(out)


def fallback_query(args: argparse.Namespace) -> int:
    graph_path = args.graph or os.path.join("graphify-out", "graph.json")
    if not os.path.isfile(graph_path):
        print("No local graph found at", graph_path, file=sys.stderr)
        return 5
    data = load_graph_json(graph_path)
    nodes = data.get("nodes", [])
    q = (args.query or "").strip()
    tokens = re.findall(r"\w+", q.lower())
    scored = []
    for n in nodes:
        s = score_node(n, tokens)
        if s > 0:
            scored.append((s, n))
    if not scored:
        # fallback substring match on label or source_file
        for n in nodes:
            lab = (n.get("label") or "").lower()
            sf = (n.get("source_file") or "").lower()
            if q.lower() in lab or q.lower() in sf:
                scored.append((1, n))
    scored.sort(key=lambda x: x[0], reverse=True)
    limit = max(1, args.limit or 10)
    repo_root = os.getcwd()
    printed = 0
    for s, n in scored[:limit]:
        printed += 1
        label = n.get("label")
        sf = n.get("source_file")
        loc = n.get("source_location") or ""
        print(f"[{printed}] {label} — {sf} {loc} (score={s})")
        print(pretty_snippet(repo_root, sf, loc, tokens))
        print("-" * 72)
    if printed == 0:
        print("No matches found for", q)
        return 6
    return 0


def main(argv: Optional[List[str]] = None) -> int:
    parser = argparse.ArgumentParser(prog="graphify_query", description="Project helper for Graphify queries")
    sub = parser.add_subparsers(dest="command")

    p_q = sub.add_parser("query", help="Query the graph for a question")
    p_q.add_argument("query", type=str)
    p_q.add_argument("--limit", "-n", type=int, default=8)
    p_q.add_argument("--budget", type=int, help="token budget forwarded to Graphify CLI")
    p_q.add_argument("--graph", type=str, help="path to graph.json (fallback)")

    p_p = sub.add_parser("path", help="Find shortest path between two nodes (uses Graphify CLI if available)")
    p_p.add_argument("a", type=str)
    p_p.add_argument("b", type=str)
    p_p.add_argument("--graph", type=str, help="path to graph.json (fallback)")

    p_e = sub.add_parser("explain", help="Explain a node and its neighbors (uses Graphify CLI if available)")
    p_e.add_argument("query", type=str)
    p_e.add_argument("--graph", type=str, help="path to graph.json (fallback)")

    args = parser.parse_args(argv)
    if not args.command:
        parser.print_help()
        return 1

    # Try CLI first
    rc = run_graphify_cli(args)
    if rc == 0:
        return 0
    # If CLI not found or failed, run fallback for query only
    if args.command == "query":
        return fallback_query(args)
    else:
        print("Graphify CLI is required for this operation (path/explain).", file=sys.stderr)
        return 7


if __name__ == "__main__":
    raise SystemExit(main())
