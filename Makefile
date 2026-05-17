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

.PHONY: help build build-ebpf build-go docker-build docker-run \
        train-ppo train-dqn bench bench-http \
        k8s-deploy k8s-teardown lint test clean

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

docker-push: docker-build ## Push image to registry
	docker push $(IMAGE)

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

k8s-deploy: docker-push ## Deploy to Kubernetes
	kubectl apply -f $(DEPLOY_DIR)/kubernetes/daemonset.yaml

k8s-teardown: ## Remove from Kubernetes
	kubectl delete -f $(DEPLOY_DIR)/kubernetes/daemonset.yaml

# ─── Quality ──────────────────────────────────────────────────────────────────

lint: ## Run linters
	cd $(GO_DIR) && go vet ./... && \
		(command -v staticcheck && staticcheck ./...) || true
	cd $(ML_DIR) && (command -v ruff && ruff check .) || true

test: ## Run Go unit tests
	cd $(GO_DIR) && go test ./... -race -timeout 60s

test-ml: ## Run Python ML module unit tests
	python -m pytest tests/ -v --tb=short

test-all: test test-ml ## Run all tests (Go + Python)

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

# ─── Clean ────────────────────────────────────────────────────────────────────

clean: ## Remove build artifacts
	rm -rf bin/ $(EBPF_DIR)/*.bpf.o
	find . -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true
	find . -name "*.pyc" -delete 2>/dev/null || true
