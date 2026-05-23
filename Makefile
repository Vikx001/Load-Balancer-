# ─── Omega-LB Makefile ────────────────────────────────────────────────────────
SHELL := /bin/bash
.DEFAULT_GOAL := help

REGISTRY    ?= omega-lb
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
IMAGE       := $(REGISTRY)/omega-lb:$(VERSION)

GO_DIR      := controlplane
ML_DIR      := ml
BENCH_DIR   := bench
EBPF_DIR    := ebpf/kern
DEPLOY_DIR  := deploy

.PHONY: help build build-ebpf build-go docker-build docker-run docker-demo \
	desktop-run desktop-build-macos desktop-build-windows \
        train-ppo train-dqn bench bench-http \
        k8s-deploy k8s-teardown lint lint-py test test-ml test-all \
        smoke reset dev health check clean download-model

help: ## Show this help
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*##/{printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ─── Build ────────────────────────────────────────────────────────────────────

build: build-ebpf build-go ## Build everything

build-ebpf: ## Compile eBPF C programs → .bpf.o
	@echo "  [eBPF] compiling kernel programs"
	$(MAKE) -C $(EBPF_DIR)

build-go: ## Build Go control-plane daemon
	@echo "  [Go] building omegalb"
	cd $(GO_DIR) && go build -ldflags="-s -w -X main.version=$(VERSION)" \
		-o ../bin/omegalb ./cmd/omegalb

# ─── Docker ───────────────────────────────────────────────────────────────────

docker-build: ## Build Docker image
	docker build -t $(IMAGE) -f $(DEPLOY_DIR)/docker/Dockerfile .

docker-run: docker-build ## Run full stack in Docker Compose
	docker compose -f $(DEPLOY_DIR)/docker/docker-compose.yml up

docker-demo: ## Run Python demo stack in Docker (no eBPF, works on macOS/Windows)
	docker compose -f $(DEPLOY_DIR)/docker/docker-compose-demo.yml up --build

docker-push: docker-build ## Push image to registry
	docker push $(IMAGE)

# ─── Desktop App ─────────────────────────────────────────────────────────────

desktop-run: ## Run native desktop launcher from source
	python3 -m venv .venv-desktop
	.venv-desktop/bin/pip install -q -r requirements.txt -r desktop/requirements.txt
	.venv-desktop/bin/python desktop/omegalb_desktop.py

desktop-build-macos: ## Build macOS app bundle (.app)
	bash desktop/build_macos.sh

desktop-build-windows: ## Build Windows executable (.exe) from PowerShell
	@echo "Run in PowerShell: powershell -ExecutionPolicy Bypass -File desktop/build_windows.ps1"

# ─── ML Training ──────────────────────────────────────────────────────────────

train-ppo: ## Train PPO + KAN actor (Layer 2+3)
	@echo "  [ML] training PPO+KAN actor"
	cd $(ML_DIR) && pip install -q -r requirements.txt && \
		python ppo/train_ppo_kan.py

train-dqn: ## Train DQN + A3C rate limiter (Layer 4)
	@echo "  [ML] training DQN+A3C rate limiter"
	cd $(ML_DIR) && pip install -q -r requirements.txt && \
		python dqn_a3c/train_dqn_a3c.py

train-all: train-ppo train-dqn ## Train all ML models

# ─── Benchmarks ───────────────────────────────────────────────────────────────

bench: ## Run simulation benchmarks (Python)
	@echo "  [bench] running simulation benchmark"
	cd $(BENCH_DIR) && python benchmark.py

bench-http: ## Run HTTP benchmarks with wrk2 (requires running instance)
	bash $(BENCH_DIR)/run_http_bench.sh

# ─── Kubernetes ───────────────────────────────────────────────────────────────

k8s-deploy: docker-push ## Deploy to Kubernetes (full privileged — use k8s-deploy-restricted for PSA clusters)
	kubectl apply -f $(DEPLOY_DIR)/kubernetes/daemonset.yaml

k8s-deploy-restricted: docker-push ## Deploy to Kubernetes with minimal capabilities (PSA/OPA-compatible)
	kubectl apply -f $(DEPLOY_DIR)/kubernetes/daemonset-restricted.yaml

k8s-deploy-fallback: ## Deploy non-eBPF NGINX fallback mode (no capabilities required)
	kubectl apply -f $(DEPLOY_DIR)/kubernetes/daemonset-fallback.yaml

k8s-teardown: ## Remove from Kubernetes
	kubectl delete -f $(DEPLOY_DIR)/kubernetes/daemonset.yaml

# ─── Staged Deployment ────────────────────────────────────────────────────────
# Each stage is independently deployable and benchmarkable.
# Do NOT advance without 2 weeks of production metrics from the current stage.
# See deploy/stages/ for per-stage config files and advance criteria.

.PHONY: stage1 stage2 stage3 stage4 stage5 stage-check

stage1: build ## Build + launch Stage 1 (eBPF + static round-robin)
	@echo "  [Stage 1] eBPF data plane + static equal-weight round-robin"
	@echo "  Config: deploy/stages/stage1-ebpf-roundrobin.yaml"
	./bin/omegalb --config deploy/stages/stage1-ebpf-roundrobin.yaml

stage2: build ## Build + launch Stage 2 (H&A ring)
	@echo "  [Stage 2] H&A consistent hash ring"
	@echo "  Config: deploy/stages/stage2-ha-ring.yaml"
	./bin/omegalb --config deploy/stages/stage2-ha-ring.yaml

stage3: build ## Build + launch Stage 3 (health checker + metrics)
	@echo "  [Stage 3] health checker + metrics + circuit breaker"
	@echo "  Config: deploy/stages/stage3-health-metrics.yaml"
	./bin/omegalb --config deploy/stages/stage3-health-metrics.yaml

stage4: build ## Build + launch Stage 4 (RL shadow mode)
	@echo "  [Stage 4] RL agent in shadow/observe mode"
	@echo "  Config: deploy/stages/stage4-rl-shadow.yaml"
	./bin/omegalb --config deploy/stages/stage4-rl-shadow.yaml

stage5: build ## Build + launch Stage 5 (RL live control)
	@echo "  [Stage 5] RL agent live traffic control"
	@echo "  WARNING: run stage 4 for 4+ weeks before advancing"
	@echo "  Config: deploy/stages/stage5-rl-live.yaml"
	./bin/omegalb --config deploy/stages/stage5-rl-live.yaml

stage-check: ## Print which stage is configured in the running daemon
	@curl -sf http://localhost:9090/metrics | grep omega_lb_stage || \
		echo "(daemon not running; check deploy/stages/*.yaml for stage config)"

# ─── Quality ──────────────────────────────────────────────────────────────────

lint: ## Run Go + Python linters
	cd $(GO_DIR) && go vet ./... && \
		(command -v staticcheck && staticcheck ./...) || true
	$(MAKE) lint-py

lint-py: ## Lint + format-check Python with ruff
	@command -v ruff >/dev/null 2>&1 || pip install ruff -q
	ruff check --output-format=concise demo/ ml/ tests/ dashboard/
	ruff format --check demo/ ml/ tests/ dashboard/

test: ## Run Go unit tests (with race detector)
	cd $(GO_DIR) && go test ./... -race -timeout 60s

test-ml: ## Run Python unit tests (unit + proxy, no integration)
	python -m pytest tests/test_all_layers.py tests/test_ml_modules.py \
		tests/test_proxy_unit.py -v --tb=short

test-all: test test-ml ## Run all tests (Go + Python)

integration: ## Run integration tests (requires .venv, starts demo stack)
	python -m pytest tests/test_integration.py -v --tb=short --timeout=60

smoke: ## 30-second end-to-end smoke test (starts + probes full demo stack)
	bash scripts/smoke_test.sh

health: ## Print live health status of all demo stack components
	bash scripts/health_check.sh

reset: ## Kill all local demo processes and reset metrics state
	bash scripts/reset.sh

dev: ## Start full demo stack (4 backends + proxy + dashboard)
	@mkdir -p demo
	@[ -f demo/live_metrics.json ] || echo '{}' > demo/live_metrics.json
	@echo "  Starting demo stack — Ctrl-C to stop"
	python demo/run.py

check: lint-py test-ml smoke ## Full local quality gate (lint + unit + smoke)

smoke-train: ## Smoke-test training pipelines (1 000 steps each, no GPU needed)
	python -c "\
from ml.ppo.train_ppo_kan import train, PPOConfig; \
c = PPOConfig(num_backends=4); c.total_steps=1000; c.rollout_steps=128; \
train(c, output_dir='/tmp/omega_smoke_models')"
	python -c "\
from ml.dqn_a3c.train_dqn_a3c import train, DQNConfig; \
c = DQNConfig(); c.total_steps=1000; \
train(c, output_dir='/tmp/omega_smoke_models')"
	@echo "  [smoke] training pipelines OK"

# ─── Models ───────────────────────────────────────────────────────────────────

MODEL_DIR   := ml/models
MODEL_URL   ?= https://github.com/Vikx001/Load-Balancer-/releases/latest/download
KAN_MODEL   := $(MODEL_DIR)/kan_actor.onnx
DQN_MODEL   := $(MODEL_DIR)/dqn_rate_limiter.onnx

.PHONY: download-model

download-model: ## Download pre-trained ONNX models from GitHub Releases
	@echo "  [models] downloading pre-trained models -> $(MODEL_DIR)/"
	@mkdir -p $(MODEL_DIR)
	@for model in kan_actor.onnx dqn_rate_limiter.onnx; do \
	    dest="$(MODEL_DIR)/$$model"; \
	    if [ -f "$$dest" ]; then \
	        echo "    $$model already present, skipping"; \
	    else \
	        echo "    downloading $$model"; \
	        curl -fsSL -o "$$dest" "$(MODEL_URL)/$$model" || \
	            { echo "    WARNING: $$model not found in releases (run make smoke-train to generate locally)"; rm -f "$$dest"; }; \
	    fi; \
	done

# ─── Clean ────────────────────────────────────────────────────────────────────

clean: ## Remove build artifacts
	rm -rf bin/ $(EBPF_DIR)/*.bpf.o
	find . -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true
	find . -name "*.pyc" -delete 2>/dev/null || true

	.PHONY: docs-graph

docs-graph: ## Regenerate local AST graph (graphify-out/) and update docs/graphify/GRAPH_REPORT.md
	@echo "  [docs] generating AST-only graph into graphify-out/ (no LLM)"
	@[ -d .venv-graphify ] || python3 -m venv .venv-graphify
	.venv-graphify/bin/pip install -q --upgrade graphifyy
	.venv-graphify/bin/graphify update . --no-cluster
	@mkdir -p docs/graphify
	cp graphify-out/GRAPH_REPORT.md docs/graphify/GRAPH_REPORT.md
	@echo "  [docs] graph ready at graphify-out/graph.json"
	@echo "  [docs] report committed to docs/graphify/GRAPH_REPORT.md"

.PHONY: dev-setup precommit-install precommit-check fmt-go

dev-setup: ## Create Python venv and install developer tooling (pre-commit, black, ruff, isort)
	@echo "  [dev] creating .venv and installing dev tools"
	@[ -d .venv ] || python3 -m venv .venv
	.venv/bin/pip install --upgrade pip
	.venv/bin/pip install --upgrade pre-commit black ruff isort isort
	.venv/bin/pre-commit install || true

precommit-install: ## Install pre-commit hooks (uses .venv if present)
	@if [ -x .venv/bin/pre-commit ]; then .venv/bin/pre-commit install; else pip install pre-commit && pre-commit install; fi

precommit-check: ## Run pre-commit checks across the repository
	@command -v .venv/bin/pre-commit >/dev/null 2>&1 && .venv/bin/pre-commit run --all-files || pre-commit run --all-files

fmt-go: ## Run gofmt across all tracked Go files
	@gofmt -s -w $(shell git ls-files '*.go')
