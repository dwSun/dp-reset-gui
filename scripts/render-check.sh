#!/usr/bin/env bash
# render-check.sh —— 无 GUI 的渲染自检
#
# 用 Fyne 软件渲染器把「屏幕排列图」渲染成 PNG（不启动任何 GUI 窗口），
# 校验输出文件存在且非空，并打印文件大小与实际像素尺寸。
#
# 用法: scripts/render-check.sh [输出 PNG 路径] [画布尺寸 WxH]
# 默认: dist/render-check.png 1000x420
# 说明: 输出默认落 dist/，不污染仓库根目录；临时文件也放 dist/ 下。
# 退出码: 0 = 渲染成功且文件非空；1 = 构建/渲染/尺寸校验失败。

set -euo pipefail

# 仓库根目录 = 脚本所在目录的上一级
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="${1:-$ROOT/dist/render-check.png}"
SIZE="${2:-1000x420}"
BIN="$ROOT/dist/dp-reset-gui"

# 沙箱里 $HOME/.cache 可能只读：未显式设置时缓存放仓库内
export GOCACHE="${GOCACHE:-$ROOT/.gocache}"

# 没有构建产物就先构建（-render 走 fyne 软件渲染器，不需要 DISPLAY）
if [[ ! -x "$BIN" ]]; then
	echo "==> 未找到 $BIN，先执行 make build"
	make build
fi

mkdir -p -- "$(dirname -- "$OUT")"
rm -f -- "$OUT"

echo "==> 软件渲染: $BIN -render $OUT -render-size $SIZE"
"$BIN" -render "$OUT" -render-size "$SIZE"

if [[ ! -s "$OUT" ]]; then
	echo "!! 渲染失败：文件不存在或为空: $OUT" >&2
	exit 1
fi

size_bytes="$(stat -c '%s' -- "$OUT")"
echo "==> 输出文件: $OUT（${size_bytes} 字节）"

# 打印 PNG 实际像素尺寸；identify 不可用时退回 file
if command -v identify >/dev/null 2>&1; then
	dims="$(identify -format '%wx%h' -- "$OUT")"
	echo "==> 图像尺寸: $dims（请求 $SIZE）"
else
	echo "==> 文件类型: $(file -b -- "$OUT")"
fi

echo "RENDER_OK"
