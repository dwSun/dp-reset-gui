#!/usr/bin/env bash
# check-leaks.sh —— 二进制路径泄漏检查
#
# 判据：构建使用 -trimpath 后，二进制里不应再带 /home/ 这类构建机绝对路径。
# 本脚本会：
#   1) 若 dist/dp-reset-gui 不存在则先构建（复用 Makefile）；
#   2) 用 strings | grep 检查二进制里是否出现 /home/ 路径；
#   3) 打印二进制大小与 sha256。
#
# 用法: scripts/check-leaks.sh [二进制路径]
# 退出码: 0 = 干净；1 = 发现 /home/ 路径或二进制缺失/构建失败。

set -euo pipefail

# 仓库根目录 = 脚本所在目录的上一级；切过去保证 make 与相对路径可用
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BIN="${1:-$ROOT/dist/dp-reset-gui}"

# 沙箱里 $HOME/.cache 可能只读：未显式设置时把 Go 构建缓存放到仓库内
export GOCACHE="${GOCACHE:-$ROOT/.gocache}"

# 二进制不存在（或不是可执行文件）时先构建
if [[ ! -x "$BIN" ]]; then
	echo "==> 未找到 $BIN，先执行 make build"
	make build
fi

if [[ ! -f "$BIN" ]]; then
	echo "!! 二进制不存在: $BIN" >&2
	exit 1
fi

# --- 基本信息：大小 + sha256 ---
size_bytes="$(stat -c '%s' -- "$BIN")"
if command -v numfmt >/dev/null 2>&1; then
	size_human="$(numfmt --to=iec --suffix=B --format='%.1f' "$size_bytes")"
else
	size_human="${size_bytes}B"
fi

echo "==> 二进制: $BIN"
echo "==> 大小:   $size_bytes 字节 (${size_human})"
echo "==> sha256: $(sha256sum -- "$BIN" | awk '{print $1}')"

# --- 泄漏检查：用 strings 提取可打印串后搜 /home/ ---
# grep 无匹配时退出码为 1，用 || true 兜住，避免 set -e 提前退出
LC_ALL=C
hits="$(strings -a -- "$BIN" | LC_ALL=C grep -n -- '/home/' || true)"

if [[ -n "$hits" ]]; then
	count="$(printf '%s\n' "$hits" | wc -l | tr -d ' ')"
	echo "!! 发现 $count 处 /home/ 绝对路径（-trimpath 可能未生效）:" >&2
	printf '%s\n' "$hits" | head -n 20 >&2
	if (( count > 20 )); then
		echo "...（仅显示前 20 处）" >&2
	fi
	exit 1
fi

echo "==> 未发现 /home/ 绝对路径（-trimpath 生效）"
echo "LEAK_CHECK_OK"
