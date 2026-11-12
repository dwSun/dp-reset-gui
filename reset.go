package main

// reset 执行层：校验目标 → 备份 monitors.xml → 提权调用 root 助手 → 重读布局校验。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ResetOptions 控制一次重抽。
type ResetOptions struct {
	UseSudo bool // 用 sudo -n 代替 pkexec
	DryRun  bool // 只校验/打印计划，不写 debugfs
	KeepOff int  // 断开后保持几秒（0 = 用 root 助手的默认值 2）
}

// ResetResult 是一次（可含多块屏的）reset 的完整结果，供 UI 呈现。
type ResetResult struct {
	Connectors     []string
	BackupPath     string
	Output         string
	Before         *State
	After          *State
	LayoutChanged  bool
	LayoutDiffText string
	LayoutChecked  bool // 是否做了布局前后比对（非 GNOME 桌面没有布局可读）
	DryRun         bool
	Err            error
}

var connIDRe = regexp.MustCompile(`^(card[0-9]+)-(DP-[0-9]+)$`)

// splitConnectorID 拆 "card0-DP-4" → ("card0", "DP-4")。
func splitConnectorID(id string) (card, name string, ok bool) {
	m := connIDRe.FindStringSubmatch(id)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// helperLang 告诉 root 助手用哪种语言输出（pkexec 会清理环境变量，所以走参数）。
func helperLang() string {
	if isChineseLocale() {
		return "zh_CN"
	}
	return "en"
}

// RunReset 对给定的接口（cardN-DP-N）做一次 DP 链路重抽。
// 多屏只需一次授权：所有接口作为参数传给同一个 root 助手。
func RunReset(connectors []string, opt ResetOptions) *ResetResult {
	res := &ResetResult{Connectors: append([]string(nil), connectors...), DryRun: opt.DryRun}
	if len(connectors) == 0 {
		res.Err = errors.New(tr("err.noAmdgpu", "no resettable connector found (needs a connected DP port on an amdgpu device)"))
		return res
	}

	// 1) 先用 sysfs 校验目标（用户态可做，避免把非法参数交给 root）。
	inv, err := Scan()
	if err != nil {
		res.Err = err
		return res
	}
	for _, id := range connectors {
		card, name, ok := splitConnectorID(id)
		if !ok {
			res.Err = fmt.Errorf("%s", tr("err.invalidConn",
				"invalid connector {{.Conn}}; only connected DP ports on an amdgpu device can be reset",
				map[string]any{"Conn": id}))
			return res
		}
		c, found := inv.Lookup(card, name)
		if !found || !c.Resettable() {
			res.Err = fmt.Errorf("%s", tr("err.invalidConn",
				"invalid connector {{.Conn}}; only connected DP ports on an amdgpu device can be reset",
				map[string]any{"Conn": id}))
			return res
		}
	}

	// 2) reset 前布局快照（读不到不阻塞重抽，只是后面没法做布局校验）。
	before, lerr := LoadLayout()
	if lerr == nil {
		res.Before = Decorate(inv, before)
		res.LayoutChecked = true
	}

	// 3) 备份 monitors.xml（mutter 在热插拔后可能重写它；旋转/坐标就存在这里）。
	if !opt.DryRun {
		res.BackupPath = backupMonitorsXML()
	}

	// 4) 提权执行 root 助手。
	_, helper, err := ensureAssets()
	if err != nil {
		res.Err = fmt.Errorf("%s", tr("err.helperpath", "cannot prepare the root helper: {{.Err}}",
			map[string]any{"Err": err.Error()}))
		return res
	}
	binName := "pkexec"
	if opt.UseSudo {
		binName = "sudo"
	}
	bin, err := exec.LookPath(binName)
	if err != nil {
		res.Err = fmt.Errorf("%s not found in PATH", binName)
		return res
	}
	args := make([]string, 0, len(connectors)+5)
	if opt.UseSudo {
		args = append(args, "-n")
	}
	args = append(args, helper, "--lang="+helperLang())
	if opt.KeepOff > 0 {
		args = append(args, fmt.Sprintf("--keep-off=%d", opt.KeepOff))
	}
	if opt.DryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, connectors...)

	// 每条链路的耗时约 = 1s + keepOff + 2s + 余量；keepOff 没指定时按助手的默认 2s 估算。
	hold := opt.KeepOff
	if hold < 2 {
		hold = 2
	}
	timeout := time.Duration(30+(15+hold)*len(connectors)) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, cerr := cmd.CombinedOutput()
	res.Output = strings.TrimSpace(string(out))
	if cerr != nil {
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			res.Err = fmt.Errorf("%s", tr("err.timeout",
				"reset timed out after {{.Timeout}}; was the authorization prompt left unanswered?",
				map[string]any{"Timeout": timeout.String()}))
		case !opt.UseSudo && exitCode(cerr) == 126:
			res.Err = fmt.Errorf("%s", tr("err.auth.denied", "authorization denied or cancelled (pkexec exit 126)"))
		case !opt.UseSudo && exitCode(cerr) == 127:
			res.Err = fmt.Errorf("%s", tr("err.auth.missing", "pkexec could not start the helper (exit 127)"))
		default:
			res.Err = fmt.Errorf("%s", tr("err.helper", "root helper failed: {{.Detail}}",
				map[string]any{"Detail": strings.TrimSpace(res.Output + " " + cerr.Error())}))
		}
		return res
	}

	// 5) 重读布局做校验（只看布局本身，不看 mutter serial）。
	if res.LayoutChecked {
		after, aerr := LoadLayout()
		if aerr != nil {
			res.Err = fmt.Errorf("%s", tr("err.after", "failed to read layout after reset: {{.Err}}",
				map[string]any{"Err": aerr.Error()}))
			return res
		}
		res.After = Decorate(inv, after)
		if sigB, sigA := res.Before.Signature(), res.After.Signature(); sigB != sigA {
			res.LayoutChanged = true
			res.LayoutDiffText = diffSignatures(res.Before, res.After)
		}
	}
	return res
}

// backupMonitorsXML 把现有 monitors.xml 复制成带时间戳的备份，返回备份路径（失败/没有则空）。
func backupMonitorsXML() string {
	src := monitorsXMLPath()
	if src == "" {
		return ""
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return ""
	}
	dst := fmt.Sprintf("%s.bak-%s", src, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return ""
	}
	return dst
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// diffSignatures 逐屏对比，给出「哪块屏的坐标/尺寸/旋转变了」的可读差异。
func diffSignatures(before, after *State) string {
	if before == nil || after == nil {
		return ""
	}
	mk := func(s *State) map[string]string {
		m := map[string]string{}
		for _, sc := range s.Screens {
			m[sc.Connector] = fmt.Sprintf("(%d,%d) %.0fx%.0f 旋转%d", sc.X, sc.Y, sc.LogicalW, sc.LogicalH, sc.Transform)
		}
		return m
	}
	b, a := mk(before), mk(after)
	var lines []string
	for conn, bs := range b {
		as, ok := a[conn]
		if !ok {
			lines = append(lines, fmt.Sprintf("  %s: 消失（reset 前 %s）", conn, bs))
			continue
		}
		if bs != as {
			lines = append(lines, fmt.Sprintf("  %s: %s → %s", conn, bs, as))
		}
	}
	for conn, as := range a {
		if _, ok := b[conn]; !ok {
			lines = append(lines, fmt.Sprintf("  %s: 新出现（%s）", conn, as))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// restoreHint 生成「怎么从备份恢复」的提示（备份为空时退化为固定文案）。
func restoreHint(backup string) string {
	if backup == "" {
		backup = filepath.Join("~", ".config", "monitors.xml")
	}
	return tr("log.restore.hint", "To restore: cp {{.Backup}} ~/.config/monitors.xml, then log out and back in",
		map[string]any{"Backup": backup})
}
