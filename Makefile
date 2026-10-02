.PHONY: build test lint vet cover clean fmt run-server run-cli memstore

GO ?= go
BIN := bin

# ── 构建 ──────────────────────────────────────────────
build: build-cli build-server build-memstore ## 构建全部产物

build-cli:
	$(GO) build -o $(BIN)/tommy-agent ./cmd/agent

build-server:
	$(GO) build -o $(BIN)/tommy-server ./cmd/server

build-memstore:
	$(GO) build -o $(BIN)/tommy-memstore ./cmd/memstore

# ── 质量检查 ──────────────────────────────────────────
fmt: ## gofmt 格式化
	gofmt -w .

vet: ## go vet 静态分析
	$(GO) vet ./...

lint: ## golangci-lint（需安装）
	golangci-lint run ./...

test: ## 运行全部测试
	$(GO) test ./... -count=1

cover: ## 测试覆盖率报告
	$(GO) test ./... -coverprofile=coverage.out -count=1
	$(GO) tool cover -func=coverage.out | tail -1
	@echo "详细报告: $(GO) tool cover -html=coverage.out"

check: fmt vet test ## 完整检查流水线（fmt + vet + test）

# ── 运行 ──────────────────────────────────────────────
run-cli: build-cli ## 运行 CLI REPL
	./$(BIN)/tommy-agent

run-server: build-server ## 运行 HTTP 服务
	./$(BIN)/tommy-server

# ── 清理 ──────────────────────────────────────────────
clean: ## 清理构建产物
	rm -rf $(BIN) coverage.out

# ── 帮助 ──────────────────────────────────────────────
help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
