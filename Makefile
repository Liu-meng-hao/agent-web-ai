# 缓存重定向到工作区：沙箱/CI 下用户目录不可写时也能构建
export GOPATH := $(CURDIR)/.gopath
export GOMODCACHE := $(GOPATH)/pkg/mod
export GOCACHE := $(CURDIR)/.gocache
export GOTELEMETRY := off

GO ?= go
EXE := $(if $(filter Windows_NT,$(OS)),.exe,)
BIN ?= bin/agent$(EXE)

.PHONY: build build-mock run test tidy fmt vet clean

build:
	$(GO) build -o $(BIN) ./cmd/agent

# 用 mock provider 的配置构建并冒烟，不需要 API key
run-mock: build
	$(BIN) run --config .tmp/mock.yaml "自检"

run: build
	$(BIN) chat

test:
	$(GO) test ./...

tidy:
	$(GO) mod tidy

fmt:
	gofmt -w ./cmd ./internal

vet:
	$(GO) vet ./...

clean:
	rm -rf bin
