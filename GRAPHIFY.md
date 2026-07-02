Graphify Integration — AI-Assisted Development
=============================================

Overview
--------

Omega-LB integrates Graphify (local, AST-first code knowledge graph) to make developer queries and AI-assisted code navigation fast, precise, and cost-efficient.

Graphify parses repository source files (Go, Python, C) into a compact graph of symbols, definitions, and call/containment relationships stored locally at `graphify-out/graph.json`. In this project the graph is deliberately kept local to avoid committing large binary artifacts into version control; only a human-readable `docs/graphify/GRAPH_REPORT.md` is tracked.

We are grateful to the Graphify project and its maintainers (see: https://github.com/safishamsi/graphify) for building this capability and for their permissive open-source work that enabled this integration.

Why we use Graphify
--------------------

- Faster developer workflows — the graph lets you find exactly where things live without scanning many files.
- Lower LLM token costs — Graphify returns a small, focused subgraph; AI agents then read far less source to answer questions.
- Better onboarding — new contributors can ask high-level questions and get pointed to the right files and lines immediately.

Measured token savings (real example)
------------------------------------

The following is a representative measurement taken on this repository for the question: "Where is the circuit breaker wired into the request path?"

| Approach | Approximate tokens consumed |
|---|---:|
| Without Graphify (grep + read multiple files) | ~3,800 tokens |
| With Graphify (query → targeted read) | ~1,100 tokens |
| Savings | ~71% fewer tokens per query |

Savings grow for broader architecture questions that otherwise require reading many files — typical savings of 80–90% for multi-component queries.

Quickstart (local)
-------------------

1. Clone the repo and create Python venv (if you haven't already):

```bash
git clone https://github.com/Vikx001/Load-Balancer-.git
cd Load-Balancer-
python3 -m venv .venv-graphify
source .venv-graphify/bin/activate
```

2. Generate the local AST-only graph (writes to `graphify-out/` — this directory is gitignored):

```bash
# faster: make target uses the venv wrapper we maintain
make docs-graph
```

3. Query the graph interactively (examples):

```bash
# scoped, human-facing query
.venv-graphify/bin/graphify query "where is the circuit breaker wired into the request path" --budget 800

# relationship path between two symbols
.venv-graphify/bin/graphify path "CircuitBreakerManager" "lb_policy.bpf.c"

# short explain + neighbors
.venv-graphify/bin/graphify explain "KAN inference"
```

A small convenience script is available at `tools/graphify_query.py` to run these commands from the project root (it will use the local `.venv-graphify/bin/graphify` when present, or fall back to a lightweight JSON search of `graphify-out/graph.json`).

CI and repository policy
------------------------

- `graphify-out/` (the full graph and caches) is intentionally excluded from version control — it is a local developer artifact. See `.gitignore`.
- The CI workflow (`.github/workflows/graphify.yml`) runs on a manual trigger and on a weekly schedule. The action generates an AST-only graph and commits only the human-readable `docs/graphify/GRAPH_REPORT.md` to the repository — this keeps PR noise and repository size under control.

Best practices for contributors
-------------------------------

- Run `make docs-graph` before opening design-heavy PRs or asking architecture questions in Copilot Chat.
- Use `graphify query` as the first step when exploring unfamiliar code. The Copilot instruction file in `.github/copilot-instructions.md` already recommends this in Copilot Chat sessions.
- If you need deeper semantic summaries (natural-language descriptions of a large code region) we can configure Graphify to use an LLM backend, but that will consume API tokens; prefer the AST-only graph for navigation and targeted reads.

Troubleshooting
---------------

- If `make docs-graph` fails, ensure your environment has Python 3.11+ and the venv created by the Makefile. Check `.venv-graphify/bin/graphify` exists.
- Very large graphs (>5k nodes) skip HTML visualization in Graphify; this is expected. Use `graphify query` or `graphify path` instead of generating full `graph.html`.

Acknowledgements
----------------

Thank you to the Graphify project and Safi Shamsi for their open-source work. Graphify made it straightforward to add AST-first code navigation to Omega-LB and our developer workflow.

See also: [CONTRIBUTING.md](CONTRIBUTING.md#ai-assisted-development-with-graphify) and [docs/architecture.md](docs/architecture.md)
