# Contributing to Omega-LB

Thank you for your interest in contributing. This document covers everything you need to get started.

---

## Table of Contents

- [Contributing to Omega-LB](#contributing-to-omega-lb)
  - [Table of Contents](#table-of-contents)
  - [AI-Assisted Development with Graphify](#ai-assisted-development-with-graphify)
    - [What Graphify Does](#what-graphify-does)
    - [Measured Token Savings](#measured-token-savings)
    - [How It Works in This Repo](#how-it-works-in-this-repo)
    - [Setting Up Graphify Locally](#setting-up-graphify-locally)
    - [Pre-commit \& Linting (recommended)](#pre-commit--linting-recommended)
    - [Using Graphify Queries Manually](#using-graphify-queries-manually)
    - [Keeping the Graph Fresh](#keeping-the-graph-fresh)
    - [Why This Matters for Contributors](#why-this-matters-for-contributors)
  - [Code of Conduct](#code-of-conduct)
  - [Getting Started](#getting-started)
    - [Prerequisites](#prerequisites)
    - [Local Setup](#local-setup)
  - [Branching Strategy](#branching-strategy)
    - [Branch Rules](#branch-rules)
    - [Creating Your Branch](#creating-your-branch)
  - [Development Workflow](#development-workflow)
  - [Commit Message Format](#commit-message-format)
    - [Types](#types)
    - [Examples](#examples)
  - [Pull Request Process](#pull-request-process)
    - [PR Checklist](#pr-checklist)
  - [Issue Reporting](#issue-reporting)
    - [Bug Reports](#bug-reports)
    - [Feature Requests](#feature-requests)
    - [Security Issues](#security-issues)
  - [Project Structure](#project-structure)
  - [Testing](#testing)
  - [Style Guidelines](#style-guidelines)
    - [Python](#python)
    - [Go](#go)
    - [eBPF (C)](#ebpf-c)
  - [Questions?](#questions)

---

## AI-Assisted Development with Graphify

> **This project uses [Graphify](https://github.com/safishamsi/graphify) — a code-first knowledge graph tool — to dramatically reduce AI token consumption during development. We strongly encourage every contributor to run Graphify locally before asking questions to an AI coding assistant.**

We extend our sincere thanks to **[Safi Shamsi](https://github.com/safishamsi)** and all Graphify contributors for building and open-sourcing this tool. Graphify has become a core part of the Omega-LB developer workflow.

### What Graphify Does

Graphify parses the entire repository using AST (Abstract Syntax Tree) extraction across Python, Go, and C source files and builds a persistent knowledge graph stored at `graphify-out/graph.json`. This graph maps every symbol, function, class, import, and call relationship across all languages and files in the repo — **without needing any LLM API key**.

When you (or an AI assistant) ask a code navigation question, instead of loading 5–10 source files to search for an answer, the agent queries the local graph and gets back only the 10–30 nodes relevant to the question. It then reads only the 1–2 files that actually contain the answer.

### Measured Token Savings

The following comparison was measured on this exact codebase for the question *"where is the circuit breaker wired into the request path?"*:

| Approach | Steps | Approximate tokens used |
|---|---|---|
| **Without Graphify** | grep → 20 matches across 10 files → read `circuit_breaker.go` (80 lines) → read `checker.go` → read `lb_policy.bpf.c` | ~3,800 tokens |
| **With Graphify** | `graphify query` → 45-node subgraph → read `circuit_breaker.go` L50–110 only | ~1,100 tokens |
| **Savings** | | **~71% fewer tokens per query** |

For broader architecture questions spanning 5+ components, the savings reach **80–90%** because the graph replaces multi-file scanning with a single compact traversal.

### How It Works in This Repo

Graphify is wired into VS Code GitHub Copilot Chat via [`.github/copilot-instructions.md`](.github/copilot-instructions.md). Those instructions tell any Copilot agent session to:

1. Run `graphify query "<your question>"` **first** when `graphify-out/graph.json` exists
2. Receive a scoped subgraph of relevant nodes pointing at exact file + line locations
3. Read only those targeted files — skipping the broad file-scanning step entirely

This is automatic in VS Code Copilot Chat. No configuration required once the graph is built locally.

### Setting Up Graphify Locally

After cloning the repo, build the local graph once:

```bash
# Generates graphify-out/graph.json (not committed to git — local only)
make docs-graph
```

### Pre-commit & Linting (recommended)

We enforce formatting and basic lint checks locally with `pre-commit`. Run the following once to install developer tooling and hooks:

```bash
make dev-setup
make precommit-install
```

Quick checks:

```bash
make precommit-check   # run pre-commit checks across the repo
make fmt-go            # format Go files in-place
```

CI will also run the same checks on PRs via `.github/workflows/lint.yml`.

Per-module Graphify and local viewer
-----------------------------------

For focused reviews or very large refactors you can build a per-module AST graph:

```bash
make docs-graph-module MODULE=controlplane/internal/health
# output: docs/graphify/GRAPH_REPORT-controlplane-internal-health.md
```

To explore the produced graph interactively, run the lightweight local viewer:

```bash
make graph-view
# then open http://localhost:8001/docs/graphify/viewer.html
```

Token-savings benchmark
------------------------

Use the included benchmark to estimate token savings for representative queries:

```bash
python tools/graphify_bench.py
```

The benchmark is approximation-based and uses a simple chars->tokens heuristic; it is intended as a reproducible project-level comparator rather than an absolute oracle.



The graph is regenerated automatically whenever you run `make docs-graph`. It takes about 30–60 seconds for the full repository. Run it again after large refactors.

> **Note:** `graphify-out/` is gitignored. The graph is a local developer artifact, not a committed build artifact. Only [`docs/graphify/GRAPH_REPORT.md`](docs/graphify/GRAPH_REPORT.md) — a human-readable summary — is tracked in version control.

### Using Graphify Queries Manually

You can query the graph directly from the terminal at any time:

```bash
# Find where a concept lives in the codebase
.venv-graphify/bin/graphify query "consistent hash ring backend selection"

# Find the relationship between two components
.venv-graphify/bin/graphify path "CircuitBreakerManager" "lb_policy.bpf.c"

# Get a focused explanation of a component and its neighbors
.venv-graphify/bin/graphify explain "KAN inference"

# Cap output to a specific token budget
.venv-graphify/bin/graphify query "RL agent rate limiter" --budget 500
```

All commands accept `--graph <path>` to point to a non-default graph location.

### Keeping the Graph Fresh

| Situation | Action |
|---|---|
| After cloning the repo | `make docs-graph` |
| After adding new files or major refactors | `make docs-graph` |
| Stale graph (old symbols) | `make docs-graph` — uses `--no-cluster` for speed |
| CI / weekly report | Automated via `.github/workflows/graphify.yml` (commits only `GRAPH_REPORT.md`) |

### Why This Matters for Contributors

Every time you open a Copilot Chat session in this workspace and ask *"how does X work?"* or *"where should I add Y?"*, the instruction file routes the question through the graph first. This means:

- **Faster answers** — the agent doesn't need to read half the repo
- **Lower API cost** — fewer tokens consumed per turn means longer, more productive sessions before hitting context limits
- **More accurate answers** — graph traversal is deterministic; the agent reads the right files, not the files that happened to match a grep pattern

We encourage all contributors — especially those working with AI coding assistants — to keep their local graph up to date and to use `graphify query` as their first step when exploring unfamiliar parts of the codebase.

---

## Code of Conduct

Be respectful. Harassment, discrimination, or personal attacks will not be tolerated. Focus on the work, not the person.

---

## Getting Started

### Prerequisites

- Python 3.13+
- Go 1.22+ (for control plane)
- Linux kernel 5.15+ with eBPF support (for full stack testing)
- macOS or Linux for local development

### Local Setup

```bash
# 1. Fork the repository on GitHub
# 2. Clone your fork
git clone https://github.com/<your-username>/Load-Balancer-.git
cd Load-Balancer-

# 3. Add upstream remote
git remote add upstream https://github.com/Vikx001/Load-Balancer-.git

# 4. Create and activate Python virtualenv
python3 -m venv .venv
source .venv/bin/activate

# 5. Install dependencies
pip install -r requirements.txt

# 6. Verify setup
python tests/test_proxy_unit.py
```

---

## Branching Strategy

This project uses a structured branching model based on [trunk-based development](https://trunkbaseddevelopment.com/) with release branches.

```
main                  ← stable, production-ready code only
  └── develop         ← integration branch, all features merge here first
        ├── feature/<name>    ← new features and enhancements
        ├── fix/<issue-id>    ← bug fixes
        ├── docs/<topic>      ← documentation-only changes
        └── chore/<topic>     ← tooling, deps, CI changes

release/v<x.y.z>      ← cut from develop when preparing a release
hotfix/<description>  ← cut from main for critical production fixes
```

### Branch Rules

| Branch | Who pushes | Merge target | Notes |
|--------|-----------|--------------|-------|
| `main` | Maintainers only | — | Protected; no direct push |
| `develop` | Maintainers only (via PR) | `main` | Integration branch |
| `feature/*` | Contributors | `develop` | Your primary working branch |
| `fix/*` | Contributors | `develop` | Bug fixes |
| `docs/*` | Contributors | `develop` | Docs-only |
| `chore/*` | Contributors | `develop` | Maintenance |
| `release/*` | Maintainers | `main` + `develop` | Release prep only |
| `hotfix/*` | Maintainers | `main` + `develop` | Critical patches |

### Creating Your Branch

```bash
# Always branch off develop, not main
git checkout develop
git pull upstream develop

# Name your branch clearly
git checkout -b feature/add-grpc-backend-support
git checkout -b fix/42-ring-rebalance-panic
git checkout -b docs/improve-ebpf-setup-guide
```

---

## Development Workflow

```
1. Open an Issue (or find an existing one)
2. Comment that you're working on it
3. Fork + branch from develop
4. Write code + tests
5. Run tests locally
6. Push branch to your fork
7. Open Pull Request → develop
8. Address review feedback
9. Maintainer merges
```

---

## Commit Message Format

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>

[optional body]

[optional footer: Closes #123]
```

### Types

| Type | When to use |
|------|-------------|
| `feat` | New feature |
| `fix` | Bug fix |
| `docs` | Documentation only |
| `test` | Adding or fixing tests |
| `refactor` | Code change with no feature/fix |
| `perf` | Performance improvement |
| `chore` | Build, CI, dependency updates |
| `ci` | CI/CD workflow changes |
| `revert` | Reverting a previous commit |

### Examples

```
feat(ring): add weighted vNode allocation for heterogeneous backends

fix(health): prevent false-positive circuit breaker trips on cold start

docs(ebpf): clarify CAP_NET_ADMIN requirement for XDP loading

Closes #47
```

---

## Pull Request Process

1. **Target branch**: Always `develop`, never `main`
2. **Title**: Follow the commit message format above
3. **Description**: Fill in the PR template (auto-populated)
4. **Tests**: All existing tests must pass; add new tests for new code
5. **Docs**: Update relevant docs if you change behavior
6. **Size**: Keep PRs focused — one logical change per PR

### PR Checklist

Before opening a PR, confirm:

- [ ] Branched from `develop`
- [ ] Tests pass locally (`python tests/test_all_layers.py`)
- [ ] No debug print statements left in code
- [ ] Docstrings updated if you changed function signatures
- [ ] `RELEASE_NOTES.md` updated if this is a notable user-facing change
- [ ] No secrets, tokens, or credentials committed

---

## Issue Reporting

### Bug Reports

Use the **Bug Report** issue template. Include:

- OS and version
- Python version (`python3 --version`)
- Exact error message and stack trace
- Steps to reproduce (minimal, numbered)
- Expected vs actual behavior

### Feature Requests

Use the **Feature Request** issue template. Include:

- Problem you're solving
- Proposed solution
- Alternatives considered
- Any relevant research papers or prior art

### Security Issues

Do **not** open a public issue for security vulnerabilities. Email the maintainer directly or use GitHub's private vulnerability reporting at:

**https://github.com/Vikx001/Load-Balancer-/security/advisories/new**

---

## Project Structure

```
controlplane/       Go control plane (eBPF loader, xDS, health, ring, ML inference)
ebpf/               eBPF kernel programs (XDP, TC hooks)
ml/                 Python ML modules (KAN, CBF, DQN+A3C, PPO)
demo/               Demo backends, proxy router, load generator
dashboard/          Streamlit metrics dashboard
desktop/            PySide6 native desktop application
deploy/             Kubernetes, Docker, Baremetal configs
tests/              Integration and unit tests
docs/               Documentation and screenshots
proto/              Protobuf definitions
bench/              Benchmarking scripts
```

Key files:
- `omega-lb.yaml` — main configuration
- `start.sh` — quick start script
- `Makefile` — build automation

---

## Testing

```bash
# Unit tests
python tests/test_proxy_unit.py

# ML module tests
python tests/test_ml_modules.py

# Integration tests (requires running stack)
python tests/test_integration.py

# Full layer test
python tests/test_all_layers.py

# Go tests (control plane)
cd controlplane && go test ./...
```

All tests must pass before a PR is mergeable.

---

## Style Guidelines

### Python

- Follow [PEP 8](https://peps.python.org/pep-0008/)
- Max line length: 100 characters
- Use type hints for all new function signatures
- Use `black` for formatting: `pip install black && black .`

### Go

- Run `gofmt` before committing
- Follow [Effective Go](https://go.dev/doc/effective_go)
- Error handling: always handle errors explicitly, no `_` discards in production paths

### eBPF (C)

- Follow Linux kernel coding style
- Comment all map operations and pointer arithmetic
- Test on kernel 5.15+ minimum

---

## Questions?

Open a [Discussion](https://github.com/Vikx001/Load-Balancer-/discussions) — not an Issue — for questions, ideas, or general feedback.
