SHELL := /bin/bash
BIN   := bin/kb
PKG   := github.com/stone/kb
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

GO ?= go
export PATH := /usr/local/go/bin:$(PATH)

.PHONY: help build install test race check fmt vet accept demo dogfood clean

help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## 构建 bin/kb
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/kb
	@echo "→ $(BIN) ($(VERSION))"

install: ## 安装到 GOPATH/bin
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/kb

test: ## 单元测试
	$(GO) test ./...

race: ## 单元测试 + 竞态检测
	$(GO) test -race ./...

fmt: ## 格式化
	$(GO) fmt ./...

vet: ## 静态检查
	$(GO) vet ./...

check: fmt vet test ## 提交前检查
	@test -z "$$(gofmt -l .)" || { echo "以下文件未格式化："; gofmt -l .; exit 1; }
	@echo "✅ 全部通过"

accept: build ## 端到端验收（会真起 git 仓库与 HTTP 服务）
	bash tests/acceptance.sh

demo: build ## 造一个带示例内容的知识库并启动服务
	bash scripts/demo.sh

dogfood: build ## kb 用自己管理自己：为本项目建知识库并装 hook
	bash scripts/dogfood.sh

clean: ## 清理构建产物与演示/自举知识库
	rm -rf bin demo-kb self-kb
