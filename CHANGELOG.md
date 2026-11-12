# Changelog

版本号遵循语义化版本；格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [0.1.0] - 2026-11-12

首个公开发布版本（从个人机器上的专用小工具整理为通用工具）。

### 新增

- GUI：自动探测 amdgpu GPU 与可重抽的 DP 接口，按桌面布局（GNOME/mutter）绘制排列图，
  点选方块或勾选列表后，一次授权重抽多条 DP 链路。
- 只读自检：`-selftest`（打印 GPU/接口/布局表）、`-dump WxH`（排列图几何）、
  `-render out.png`（Fyne 软件渲染器出图，无需显示器）。
- `-dry-run` 干跑（只校验并打印计划，不写 debugfs）、`-keep-off N`（断开保持秒数）、
  `-use-sudo`（免密 sudo 代替 pkexec）、`-version`。
- 中英双语界面：基于 Fyne 的 `lang` 包，按系统 locale 自动选择（`zh_CN` → 中文，其余 → 英文）。
- 非 GNOME 桌面的降级模式：读不到 mutter 布局时不画排列图、只列接口，重抽仍可用。
- 重抽前自动备份 `monitors.xml`；重抽后重读布局比对坐标/缩放/旋转，变化时提示恢复。
- root 助手：内嵌进二进制、按需释放；先全量校验再写 debugfs，只接受 amdgpu 上
  `status=connected` 的 `DP-N`；debugfs 只读时给出 remount 提示。
- 构建与发布：Makefile（`build` / `test` / `vet` / `fmt-check` / `selftest` / `render` /
  `leaks` / `release` / `clean`）、`-trimpath -s -w` 构建、二进制路径泄漏检查、MIT LICENSE、
  `.gitignore`、中英双语文档与排列图示例。

### 说明

- 重抽是**概率性缓解**，不是根治；链路是否重新锁对只能由人眼确认。
- 仅适用于 amdgpu 的 DisplayPort 输出；eDP、HDMI、MST 子接口不在范围内。
