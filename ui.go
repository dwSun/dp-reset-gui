package main

// 界面装配：排列图 + 接口勾选 + 日志。所有文案走 i18n（tr）。
// 布局后端不可用（非 GNOME）时自动降级：排列图隐藏，只保留接口列表，仍可重抽。

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type ui struct {
	app  fyne.App
	win  fyne.Window
	opt  ResetOptions
	font string

	arr       *Arrangement
	state     *State
	inv       *Inventory
	checks    map[string]*widget.Check // key = cardN-DP-N
	checksBox *fyne.Container
	gpuLabel  *widget.Label
	status    *widget.Label
	logLabel  *widget.Label
	resetBtn  *widget.Button
	busy      bool
	syncing   bool
}

// cjkTheme 只替换字体、其余全部委托给内置主题。
// 注意：必须走 Theme 实现——FYNE_FONT 环境变量只影响一部分控件，
// canvas.Text（排列图里的标签）不会用它，所以之前图上中文是方块。
type cjkTheme struct {
	base fyne.Theme
	font fyne.Resource
}

func (t cjkTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	return t.base.Color(n, v)
}

func (t cjkTheme) Font(_ fyne.TextStyle) fyne.Resource { return t.font }

func (t cjkTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return t.base.Icon(n) }

func (t cjkTheme) Size(n fyne.ThemeSizeName) float32 { return t.base.Size(n) }

// applyFont 把字体注入主题，使 canvas.Text 与普通控件统一使用它。
func applyFont(a fyne.App, path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	res := fyne.NewStaticResource(filepath.Base(path), data)
	a.Settings().SetTheme(cjkTheme{base: theme.DefaultTheme(), font: res})
	return true
}

func newUI(a fyne.App, opt ResetOptions, font string) *ui {
	fontOK := applyFont(a, font)
	u := &ui{app: a, opt: opt, font: font, checks: map[string]*widget.Check{}}

	u.win = a.NewWindow(tr("app.title", "DP Link Reset — eGPU dock display fix"))
	u.win.Resize(fyne.NewSize(1020, 900))

	// 排列图上点了某块 → 同步下面的勾选控件
	u.arr = NewArrangement(func(device string) {
		u.syncing = true
		if c, ok := u.checks[device]; ok {
			c.SetChecked(u.arr.selected[device])
		}
		u.syncing = false
	})

	hint := widget.NewLabel(tr("app.hint",
		"Click a block in the layout, or tick a screen below. Grey blocks cannot be reset (not DP, not connected, or not on an amdgpu device)."))
	hint.Wrapping = fyne.TextWrapWord

	u.gpuLabel = widget.NewLabel("")
	u.gpuLabel.Wrapping = fyne.TextWrapWord

	u.checksBox = container.NewVBox()
	checksScroll := container.NewVScroll(u.checksBox)
	checksScroll.SetMinSize(fyne.NewSize(0, 170))

	u.status = widget.NewLabel("")
	u.status.Wrapping = fyne.TextWrapWord

	u.logLabel = widget.NewLabel("")
	u.logLabel.Wrapping = fyne.TextWrapWord
	logScroll := container.NewVScroll(u.logLabel)
	logScroll.SetMinSize(fyne.NewSize(0, 150))

	u.resetBtn = widget.NewButton(tr("btn.reset", "Reset selected links"), u.onReset)
	u.resetBtn.Importance = widget.HighImportance
	refreshBtn := widget.NewButton(tr("btn.reload", "Reload layout"), func() { u.reload() })
	allBtn := widget.NewButton(tr("btn.selectAll", "Select all"), func() { u.selectAll(true) })
	noneBtn := widget.NewButton(tr("btn.clear", "Clear"), func() { u.selectAll(false) })
	buttons := container.NewHBox(u.resetBtn, refreshBtn, allBtn, noneBtn)

	bottom := container.NewVBox(
		widget.NewSeparator(),
		boldLabel(tr("section.resettable", "Resettable screens")),
		checksScroll,
		buttons,
		u.status,
		widget.NewSeparator(),
		boldLabel(tr("section.log", "Log")),
		logScroll,
	)

	top := container.NewVBox(
		boldLabel(tr("app.heading", "DP link reset (one or more screens)")),
		hint,
		u.gpuLabel,
		widget.NewSeparator(),
	)

	u.win.SetContent(container.NewBorder(top, bottom, nil, nil, u.arr))

	if opt.UseSudo {
		u.appendLog(tr("log.privilege.sudo", "Privilege: sudo -n (passwordless sudo)"))
	} else {
		u.appendLog(tr("log.privilege.pkexec", "Privilege: pkexec (system prompt on each reset)"))
	}
	if fontOK {
		u.appendLog(tr("log.font", "UI font: {{.Font}}", map[string]any{
			"Font": tr("log.font.injected", "{{.Font}} (injected into theme)", map[string]any{"Font": font}),
		}))
	} else {
		u.appendLog(tr("log.font", "UI font: {{.Font}}", map[string]any{
			"Font": tr("log.font.builtin", "Fyne built-in (no CJK glyphs; Chinese may show as boxes)"),
		}))
	}
	if opt.DryRun {
		u.appendLog(tr("log.dryRun", "DRY RUN: nothing is written to debugfs"))
	}

	u.reload()
	return u
}

// reload 重新探测设备、读取布局并刷新排列图 / 勾选列表 / GPU 信息。
func (u *ui) reload() {
	inv, err := Scan()
	if err != nil {
		u.status.SetText(tr("cli.err.layout", "failed to read layout: {{.Err}}", map[string]any{"Err": err.Error()}))
		u.appendLog(tr("log.layout.fail", "Failed to read layout: {{.Err}}", map[string]any{"Err": err.Error()}))
		return
	}
	u.inv = inv

	source := "mutter"
	st, lerr := LoadLayout()
	if lerr != nil {
		// 非 GNOME 桌面：没有布局可读，降级成「只列接口」，重抽本身仍然可用。
		st = FallbackState(inv, lerr)
		source = "sysfs"
		u.appendLog(tr("log.layout.fail", "Failed to read layout: {{.Err}}", map[string]any{"Err": lerr.Error()}))
	} else {
		Decorate(inv, st)
	}
	u.state = st
	u.arr.SetScreens(st.Screens)
	u.arr.SetNoLayout(!st.LayoutOK)
	u.rebuildChecks(st)
	u.gpuLabel.SetText(u.renderGPUInfo(inv, source))
	u.status.SetText(tr("status.summary", "{{.Total}} screen(s), {{.Resettable}} resettable", map[string]any{
		"Total": len(st.Screens), "Resettable": len(st.Resettable()),
	}))
	u.arr.Refresh()
}

// renderGPUInfo 拼出「检测到的 GPU / 布局来源」提示行。
func (u *ui) renderGPUInfo(inv *Inventory, source string) string {
	lines := make([]string, 0, len(inv.GPUs)+1)
	for _, g := range inv.GPUs {
		if g.Driver == "" {
			continue
		}
		line := tr("gpu.line", "GPU {{.BDF}} ({{.Driver}})", map[string]any{
			"BDF": orDash(g.BDF), "Driver": g.Driver,
		})
		if g.Hotplug {
			line += tr("gpu.hotplug", " · hot-pluggable dock GPU")
		} else if hasInternalPanel(inv, g.Card) {
			line += tr("gpu.internal", " · built-in")
		}
		lines = append(lines, line)
	}
	lines = append(lines, tr("log.layout.source", "Layout source: {{.Source}}", map[string]any{"Source": source}))
	return strings.Join(lines, "\n")
}

// rebuildChecks 按当前布局重建勾选列表（顺序：从上到下、从左到右）。
func (u *ui) rebuildChecks(st *State) {
	u.checks = map[string]*widget.Check{}
	u.checksBox.RemoveAll()

	screens := append([]Screen{}, st.Resettable()...)
	sort.SliceStable(screens, func(i, j int) bool {
		if screens[i].Y != screens[j].Y {
			return screens[i].Y < screens[j].Y
		}
		if screens[i].X != screens[j].X {
			return screens[i].X < screens[j].X
		}
		return screens[i].Connector < screens[j].Connector
	})

	// 编号与排列图一致：全部屏按 (y,x) 编号 1..N；列表只列可重抽的（会跳号）。
	num := make(map[string]int, len(st.Screens))
	for i, sc := range st.Screens {
		num[sc.DeviceID()] = i + 1
	}

	for _, s := range screens {
		id := s.DeviceID()
		c := widget.NewCheck(u.checkLabel(num[id], s), func(on bool) {
			if u.syncing {
				return
			}
			u.arr.SetSelected(id, on)
		})
		c.SetChecked(u.arr.selected[id])
		u.checks[id] = c
		u.checksBox.Add(c)
	}
	u.checksBox.Refresh()
}

// checkLabel 生成勾选项文案：编号 / 接口 / 型号 / 分辨率 / 旋转 / 卡。
func (u *ui) checkLabel(n int, s Screen) string {
	parts := []string{fmt.Sprintf("%d.", n), s.Connector}
	if p := strings.TrimSpace(s.Product); p != "" {
		parts = append(parts, p)
	}
	if s.PxW > 0 && s.PxH > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", s.PxW, s.PxH))
	}
	if s.Transform != 0 {
		parts = append(parts, rotationLabel(s.Transform))
	}
	if s.Card != "" {
		parts = append(parts, s.Card)
	}
	return strings.Join(parts, "  ")
}

func (u *ui) selectAll(on bool) {
	u.syncing = true
	for id, c := range u.checks {
		c.SetChecked(on)
		u.arr.SetSelected(id, on)
	}
	u.syncing = false
	u.arr.Refresh()
}

// onReset 走「确认 → 提权执行 → 重读布局校验」。
func (u *ui) onReset() {
	if u.busy {
		return
	}
	ids := u.arr.Selected()
	if len(ids) == 0 {
		dialog.ShowInformation(tr("dialog.noSelection.title", "Nothing selected"),
			tr("dialog.noSelection.body", "Click a block in the layout, or tick a screen below."), u.win)
		return
	}
	sort.Strings(ids)
	msg := tr("dialog.confirm.body",
		"A software unplug/replug will be performed on {{.Count}} link(s):\n\n  {{.Conns}}\n\nThose screens blank briefly; other screens are unaffected.\n~/.config/monitors.xml is backed up automatically. Continue?",
		map[string]any{"Count": len(ids), "Conns": strings.Join(ids, "  ")})
	dialog.ShowConfirm(tr("dialog.confirm.title", "Confirm reset"), msg, func(ok bool) {
		if ok {
			u.runReset(ids)
		}
	}, u.win)
}

func (u *ui) runReset(ids []string) {
	u.busy = true
	u.resetBtn.Disable()
	u.resetBtn.SetText(tr("btn.reset.busy", "Working… (waiting for authorization / retraining)"))
	u.appendLog(tr("log.reset.start", "==== Reset: {{.Conns}} ====", map[string]any{"Conns": strings.Join(ids, " ")}))
	if u.opt.DryRun {
		u.appendLog(tr("log.dryRun", "DRY RUN: nothing is written to debugfs"))
		u.appendLog(tr("log.plan", "planned: {{.Conns}}", map[string]any{"Conns": strings.Join(ids, " ")}))
	}

	go func() {
		res := RunReset(ids, u.opt)
		fyne.Do(func() {
			u.busy = false
			u.resetBtn.Enable()
			u.resetBtn.SetText(tr("btn.reset", "Reset selected links"))
			u.renderResult(res)
			u.reload()
		})
	}()
}

func (u *ui) renderResult(res *ResetResult) {
	if res.BackupPath != "" {
		u.appendLog(tr("log.backup", "Backed up monitors.xml → {{.Path}}", map[string]any{"Path": res.BackupPath}))
	}
	if res.Output != "" {
		u.appendLog(tr("log.helper.output", "root helper output:") + "\n" + indent(res.Output))
	}
	if res.Err != nil {
		u.appendLog(tr("log.error", "❌ {{.Err}}", map[string]any{"Err": res.Err.Error()}))
		dialog.ShowError(res.Err, u.win)
		return
	}
	if !res.LayoutChecked {
		u.appendLog(tr("log.layout.skipped", "Layout verification skipped: no desktop layout backend."))
		return
	}
	if res.LayoutChanged {
		u.appendLog(tr("log.layout.changed", "⚠️ Layout changed (mutter rewrote the config):") + "\n" + res.LayoutDiffText)
		u.appendLog(restoreHint(res.BackupPath))
		dialog.ShowInformation(tr("dialog.changed.title", "Layout changed"),
			tr("dialog.changed.body",
				"Layout changed after reset:\n\n{{.Diff}}\n\nBackup:\n{{.Backup}}\n\nTo restore: copy it over ~/.config/monitors.xml, then log out and back in.",
				map[string]any{"Diff": res.LayoutDiffText, "Backup": res.BackupPath}), u.win)
		return
	}
	u.appendLog(tr("log.layout.same",
		"✅ Layout unchanged (position/scale/rotation preserved). Look at the screen: if it is still broken, reset again — link retraining is probabilistic."))
}

func (u *ui) appendLog(s string) {
	cur := u.logLabel.Text
	if cur != "" {
		cur += "\n"
	}
	u.logLabel.SetText(cur + s)
}

// boldLabel 生成小节标题。
func boldLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
