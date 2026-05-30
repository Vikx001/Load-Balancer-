# Contributing to Omega-LB

Thank you for your interest in contributing. This document covers everything you need to get started.

---

## Table of Contents

- [Contributing to Omega-LB](#contributing-to-omega-lb)
  - [Table of Contents](#table-of-contents)
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
  > Note: Small `print()` calls are allowed in standalone CLI or demo scripts
  > (e.g., `demo/` or `dist/` tools) for interactive/user-facing messages.
  > Avoid `print()` in library, dashboard, or production-facing modules; prefer
  logging for structured output.
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
