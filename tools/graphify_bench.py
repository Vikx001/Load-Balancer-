#!/usr/bin/env python3
"""tools/graphify_bench.py

Estimate token savings (approximate) for a set of representative queries
by comparing a naive grep+read approach vs a Graphify-guided targeted read.

This is intentionally dependency-free and uses a simple chars->tokens heuristic
(1 token ≈ 4 characters) to keep the script runnable in the repository without
installing extra packages.

Usage:
  python tools/graphify_bench.py            # run default queries
  python tools/graphify_bench.py --queries queries.txt

The script expects a local `graphify-out/graph.json` produced by running
`make docs-graph` beforehand.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from typing import List, Tuple


def approx_tokens(text: str) -> int:
    return max(1, int(len(text) / 4))


def repo_source_files(root: str = ".") -> List[str]:
    exts = {".py", ".go", ".c", ".h", ".proto", ".md", ".yaml", ".yml"}
    out = []
    for dp, dns, files in os.walk(root):
        # skip common noisy dirs
        if "/.git/" in dp or dp.startswith("./.git"):
            continue
        if "graphify-out" in dp or dp.startswith("./.venv") or "/.venv" in dp:
            continue
        if dp.startswith("./docs/") and "graphify" not in dp:
            # skip general docs, keep docs/graphify
            continue
        for f in files:
            if f.startswith("."):
                continue
            _, e = os.path.splitext(f)
            if e in exts:
                out.append(os.path.join(dp, f))
    return out


def find_grep_matches(query: str, files: List[str], limit: int = 10) -> List[Tuple[int, str, str]]:
    tokens = re.findall(r"\w+", query.lower())
    matches = []
    for p in files:
        try:
            txt = open(p, "r", encoding="utf-8", errors="ignore").read().lower()
        except Exception:
            continue
        score = sum(txt.count(t) for t in tokens)
        if score > 0:
            matches.append((score, p, txt))
    matches.sort(key=lambda x: x[0], reverse=True)
    return matches[:limit]


def baseline_token_estimate(matches: List[Tuple[int, str, str]]) -> Tuple[int, int]:
    total = 0
    files_read = 0
    for _score, path, txt in matches:
        total += approx_tokens(txt)
        files_read += 1
    # account for prompt/context overhead (approx)
    total += 150
    return total, files_read


def load_graph_nodes(graph_path: str = "graphify-out/graph.json") -> List[dict]:
    if not os.path.isfile(graph_path):
        return []
    with open(graph_path, "r", encoding="utf-8") as f:
        data = json.load(f)
    return data.get("nodes", [])


def score_node(node: dict, tokens: List[str]) -> int:
    label = (node.get("label") or "").lower()
    source = (node.get("source_file") or "").lower()
    nid = (node.get("id") or "").lower()
    score = 0
    for t in tokens:
        if t:
            score += 4 * label.count(t)
            score += 2 * source.count(t)
            score += 1 * nid.count(t)
    return score


def graph_targeted_estimate(query: str, nodes: List[dict], limit: int = 10) -> Tuple[int, int]:
    tokens = re.findall(r"\w+", query.lower())
    scored = []
    for n in nodes:
        s = score_node(n, tokens)
        if s > 0:
            scored.append((s, n))
    if not scored:
        # no nodes matched; return a conservative estimate
        return 200, 0
    scored.sort(key=lambda x: x[0], reverse=True)
    total = 0
    files_seen = set()
    read_snippets = 0
    for s, n in scored[:limit]:
        sf = n.get("source_file") or ""
        loc = n.get("source_location") or n.get("loc") or ""
        if not sf or not os.path.isfile(sf):
            continue
        try:
            with open(sf, "r", encoding="utf-8", errors="ignore") as f:
                lines = f.readlines()
        except Exception:
            continue
        m = re.search(r"L(\d+)", loc or "")
        line_no = int(m.group(1)) if m else 1
        start = max(0, line_no - 5)
        end = min(len(lines), line_no + 4)
        snippet = "".join(lines[start:end])
        # ensure each file counts only once
        if sf not in files_seen:
            total += approx_tokens(snippet)
            files_seen.add(sf)
            read_snippets += 1
    # include a small overhead for the graph query/summary
    total += 60
    return total, read_snippets


def run_bench(queries: List[str], limit: int = 10, graph_path: str = "graphify-out/graph.json") -> int:
    files = repo_source_files(".")
    nodes = load_graph_nodes(graph_path)
    if not nodes:
        print("No graph nodes found at", graph_path, file=sys.stderr)
        print("Run `make docs-graph` first to generate `graphify-out/graph.json`.", file=sys.stderr)
        return 2

    results = []
    for q in queries:
        print("\n=== Query:", q)
        grep_matches = find_grep_matches(q, files, limit=limit)
        baseline_tokens, files_read = baseline_token_estimate(grep_matches)
        graph_tokens, snippets = graph_targeted_estimate(q, nodes, limit=limit)
        savings = 0.0
        if baseline_tokens > 0:
            savings = 100.0 * (baseline_tokens - graph_tokens) / baseline_tokens
        print(f"Baseline tokens (grep+read top {files_read} files + overhead): {baseline_tokens}")
        print(f"Graph-assisted tokens (targeted snippets {snippets} + overhead): {graph_tokens}")
        print(f"Approx. savings: {savings:.1f}%")
        results.append((q, baseline_tokens, graph_tokens, savings))

    print("\nSummary:")
    for q, b, g, s in results:
        print(f"- {q[:60]:60s} | baseline {b:6d}  graph {g:6d}  savings {s:5.1f}%")

    avg_saving = sum(r[3] for r in results) / max(1, len(results))
    print(f"\nAverage savings: {avg_saving:.1f}%")
    return 0


def main(argv: List[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Rudimentary Graphify token-savings benchmark")
    parser.add_argument("--queries", "-q", help="File with newline-separated queries")
    parser.add_argument("--limit", "-n", type=int, default=10, help="Top files / nodes to consider")
    parser.add_argument("--graph", default="graphify-out/graph.json", help="Path to graph.json")
    args = parser.parse_args(argv)

    if args.queries:
        if not os.path.isfile(args.queries):
            print("Queries file not found:", args.queries, file=sys.stderr)
            return 2
        with open(args.queries, "r", encoding="utf-8") as f:
            queries = [line.strip() for line in f if line.strip()]
    else:
        queries = [
            "Where is the circuit breaker wired into the request path?",
            "Where is the KAN inference implemented?",
            "Where is the ring reconcile logic?",
            "Where is the eBPF loader invoked?",
        ]

    return run_bench(queries, limit=args.limit, graph_path=args.graph)


if __name__ == "__main__":
    raise SystemExit(main())
