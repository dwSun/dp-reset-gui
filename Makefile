# dp-reset-gui 构建入口（GNU make）
#
# 常用目标：
#   make build      构建 dist/dp-reset-gui（cgo + Fyne 的 GUI 二进制）
#   make selftest   只读自检：打印读到的屏幕布局后退出（不启动 GUI）
#   make render     用软件渲染器出图到 dist/render-check.png（不启动 GUI）
#   make leaks      检查二进制里没有 /home/ 绝对路径（-trimpath 是否生效的判据）
#   make release    构建 + 生成 dist/SHA256SUMS + 泄漏检查
#
# 所有变量都可在命令行覆盖，例如：
#   make build VERSION=v1.0.0 COMMIT=abc1234
#   make build GOCACHE=$(pwd)/.gocache
#
# 本 Makefile 只调用只读 git 命令（describe / rev-parse），不做任何 git 写操作；
# 拿不到版本信息时自动回退为 dev / none。

GO      ?= go
GIT     ?= git
GOFMT   ?= gofmt
BIN     ?= dp-reset-gui
DIST    ?= dist
OUT     ?= $(DIST)/$(BIN)
SCRIPTS ?= scripts
ARGS    ?=
RENDER_SIZE ?= 1000x420

# 版本信息：git describe / rev-parse 失败（含仓库不存在、无 git）时优雅回退
VERSION ?= $(shell $(GIT) describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell $(GIT) rev-parse --short HEAD 2>/dev/null || echo none)

# 构建缓存：沙箱里 $HOME/.cache 往往只读，未显式指定时默认放仓库内 .gocache/。
# 优先级：make 命令行 > 环境变量 > 这里（用 ?= 保证可覆盖）。
GOCACHE ?= $(CURDIR)/.gocache
export GOCACHE

# Fyne 依赖 OpenGL/X11，必须开启 cgo
CGO_ENABLED ?= 1
export CGO_ENABLED

# 依赖已进模块缓存时，建议配合 `export GOPROXY=off` 做完全离线构建
GOFLAGS ?= -mod=mod
export GOFLAGS

LDFLAGS ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: all build test vet fmt fmt-check run selftest render leaks release clean help

all: build

$(DIST):
	@mkdir -p $(DIST)

build: $(DIST)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT) .
	@echo "==> 已构建 $(OUT)（VERSION=$(VERSION)，COMMIT=$(COMMIT)）"

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# fmt 会直接改写源文件，只在本地用；提交/CI 前用 fmt-check 只检查不改
fmt:
	$(GOFMT) -l -w .

fmt-check:
	@out="$$($(GOFMT) -l .)"; \
	if [ -n "$$out" ]; then \
		echo "!! 以下文件未通过 gofmt（可运行 make fmt 修复）："; \
		printf '%s\n' "$$out"; \
		exit 1; \
	fi; \
	echo "==> gofmt 检查通过"

run: build
	$(OUT) $(ARGS)

selftest: build
	$(OUT) -selftest

render: build
	bash $(SCRIPTS)/render-check.sh "$(DIST)/render-check.png" "$(RENDER_SIZE)"

leaks: build
	bash $(SCRIPTS)/check-leaks.sh "$(OUT)"

# release = 构建 + 校验和 + 泄漏检查；直接调用脚本以免重复 build
release: build
	@cd $(DIST) && sha256sum $(BIN) > SHA256SUMS && echo "==> dist/SHA256SUMS:" && cat SHA256SUMS
	@bash $(SCRIPTS)/check-leaks.sh "$(OUT)"

clean:
	rm -rf $(DIST)

help:
	@echo '可用目标:'
	@echo '  build      构建 $(OUT)'
	@echo '  test       go test ./...'
	@echo '  vet        go vet ./...'
	@echo '  fmt        gofmt -l -w .（会改写源文件）'
	@echo '  fmt-check  只检查 gofmt，不修改'
	@echo '  run        构建后直接运行（可传 ARGS="..."）'
	@echo '  selftest   只读自检，打印屏幕布局'
	@echo '  render     软件渲染 PNG 到 $(DIST)/render-check.png'
	@echo '  leaks      检查二进制里是否残留 /home/ 绝对路径'
	@echo '  release    构建 + dist/SHA256SUMS + 泄漏检查'
	@echo '  clean      删除 $(DIST)/'
