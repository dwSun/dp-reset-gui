package main

// dp-reset-gui —— 外接显卡扩展坞「整屏错位 / 拼接线 / 光标一起错」的一键修复。
//
// 原理：对出问题的 amdgpu DP 链路做一次「软件拔插」
// （debugfs 的 DP-N/trigger_hotplug 写 0 → 等 → 写 1），让链路重新训练、
// 接收端重新锁定。不是根治，而是「再赌一次」——重抽是概率事件。
//
// 设计原则：不写死任何机器专属信息。GPU / PCI 地址 / 可重抽接口全部从
// sysfs 自动探测（见 discover.go），root 助手自己再校验一遍。
//
// 入口分两类：
//   - GUI：普通用户运行，pkexec/sudo 提权后调用内嵌 root 助手；
//   - 只读自检：-selftest / -dump / -render，不需要 root、不碰屏幕。

import (
	"flag"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/test"
)

// 由构建脚本 -ldflags "-X main.version=... -X main.commit=..." 注入。
var (
	version = "dev"
	commit  = "none"
)

func main() {
	// 必须在任何 tr() 之前加载翻译包（flag 的帮助文本也要本地化）。
	loadTranslations()

	selftest := flag.Bool("selftest", false, tr("flag.selftest", "read-only: print detected layout/connectors and exit"))
	useSudo := flag.Bool("use-sudo", false, tr("flag.useSudo", "use 'sudo -n' instead of pkexec"))
	dryRun := flag.Bool("dry-run", false, tr("flag.dryRun", "validate and print the plan without writing to debugfs"))
	keepOff := flag.Int("keep-off", 0, tr("flag.keepOff", "seconds to keep the link down before retraining (default 2)"))
	fontPath := flag.String("font", "", tr("flag.font", "UI font file (default: auto-detect a CJK font)"))
	dumpSpec := flag.String("dump", "", tr("flag.dump", "read-only: print arrangement rectangles for a canvas size (e.g. 900x300)"))
	renderOut := flag.String("render", "", tr("flag.render", "read-only: render the arrangement to a PNG (software renderer)"))
	renderSize := flag.String("render-size", "900x300", tr("flag.renderSize", "canvas size for -render"))
	showVersion := flag.Bool("version", false, tr("flag.version", "print version and exit"))
	flag.Parse()

	if *showVersion {
		fmt.Println(tr("version.line", "{{.Name}} {{.Version}} (commit {{.Commit}}, {{.Go}})", map[string]any{
			"Name": "dp-reset-gui", "Version": version, "Commit": commit, "Go": runtime.Version(),
		}))
		return
	}

	// Fyne 内置字体不含中文，必须换字体，否则中文全是方块。
	font := *fontPath
	if font == "" {
		font = pickCJKFont()
	}
	// 字体注入统一走自定义 Theme（见 ui.go 的 applyFont）。
	// 刻意不设 FYNE_FONT：它只部分生效，而且 Fyne 会据此去找 Bold/Italic 变体，
	// 中文 ttf 往往没有这些变体，会在日志里刷 "Error loading specified font"。

	opt := ResetOptions{UseSudo: *useSudo, DryRun: *dryRun, KeepOff: *keepOff}

	switch {
	case *renderOut != "":
		if err := runRender(*renderOut, *renderSize, font); err != nil {
			fmt.Fprintln(os.Stderr, tr("cli.err.render", "render failed: {{.Err}}", map[string]any{"Err": err.Error()}))
			os.Exit(1)
		}
		fmt.Println(tr("cli.render.done", "wrote {{.Path}}", map[string]any{"Path": *renderOut}))
	case *dumpSpec != "":
		if err := runDump(*dumpSpec); err != nil {
			fmt.Fprintln(os.Stderr, tr("cli.err.layout", "failed to read layout: {{.Err}}", map[string]any{"Err": err.Error()}))
			os.Exit(1)
		}
	case *selftest:
		if err := runSelftest(); err != nil {
			fmt.Fprintln(os.Stderr, tr("cli.err.layout", "failed to read layout: {{.Err}}", map[string]any{"Err": err.Error()}))
			os.Exit(1)
		}
	default:
		a := app.New()
		u := newUI(a, opt, font)
		u.win.ShowAndRun()
	}
}

// runSelftest 打印探测到的 GPU / 可重抽接口 / 桌面布局，均为只读。
func runSelftest() error {
	inv, invErr := Scan()
	if invErr != nil {
		fmt.Fprintln(os.Stderr, invErr)
	}
	if inv != nil {
		for _, g := range inv.GPUs {
			line := tr("cli.selftest.gpu", "GPU {{.Card}}  {{.BDF}}  {{.Driver}}", map[string]any{
				"Card": g.Card, "BDF": orDash(g.BDF), "Driver": orDash(g.Driver),
			})
			if g.Hotplug {
				line += tr("gpu.hotplug", " · hot-pluggable dock GPU")
			} else if hasInternalPanel(inv, g.Card) {
				line += tr("gpu.internal", " · built-in")
			}
			fmt.Println(line)
		}
		res := inv.Resettable()
		if len(res) == 0 {
			fmt.Println(tr("cli.selftest.none", "no resettable connector found"))
		} else {
			names := make([]string, 0, len(res))
			for _, c := range res {
				names = append(names, c.Device)
			}
			fmt.Println(tr("cli.selftest.connectors", "resettable connectors: {{.Conns}}", map[string]any{
				"Conns": strings.Join(names, " "),
			}))
		}
	}

	st, err := LoadLayout()
	if err != nil {
		fmt.Println(tr("cli.selftest.noLayout", "desktop layout unavailable: {{.Err}}", map[string]any{"Err": err.Error()}))
		return nil
	}
	if inv != nil {
		Decorate(inv, st)
	}
	printState(st)
	return nil
}

// printState 以表格形式打印布局快照（CLI 用，列名保持 ASCII 方便脚本处理）。
func printState(st *State) {
	fmt.Println(tr("cli.selftest.header", "mutter serial={{.Serial}}  screens={{.Count}}", map[string]any{
		"Serial": st.Serial, "Count": len(st.Screens),
	}))
	fmt.Printf("  %-2s %-9s %-7s %-24s %-13s %-5s %-13s %-13s %-6s %-10s %s\n",
		"#", "CONNECTOR", "CARD", "PRODUCT", "MODE", "ROT", "POS", "LOGICAL", "SCALE", "STATUS", "FLAGS")
	for i, s := range st.Screens {
		flags := make([]string, 0, 2)
		if s.Resettable {
			flags = append(flags, tr("cli.selftest.resettable", "resettable"))
		}
		if s.Primary {
			flags = append(flags, tr("cli.selftest.primary", "[primary]"))
		}
		product := strings.TrimSpace(s.Product)
		if product == "" {
			product = "-"
		}
		fmt.Printf("  %-2d %-9s %-7s %-24s %-13s %-5s %-13s %-13s %-6.3g %-10s %s\n",
			i+1, s.Connector, orDash(s.Card), product,
			fmt.Sprintf("%dx%d", s.PxW, s.PxH), rotationCompact(s.Transform),
			fmt.Sprintf("(%d,%d)", s.X, s.Y),
			fmt.Sprintf("%.0fx%.0f", s.LogicalW, s.LogicalH), s.Scale,
			orDash(s.Status), strings.Join(flags, " "))
	}
}

// runDump 打印排列图在指定画布尺寸下每块屏的绘制矩形，用来验证缩放/居中/命中判定。
func runDump(spec string) error {
	st, err := LoadLayout()
	if err != nil {
		return err
	}
	if inv, ierr := Scan(); ierr == nil {
		Decorate(inv, st)
	}
	var w, h int
	if _, err := fmt.Sscanf(spec, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return fmt.Errorf("bad size %q, example: -dump 900x300", spec)
	}
	size := fyne.NewSize(float32(w), float32(h))
	fmt.Println(tr("cli.dump.header", "canvas {{.W}}x{{.H}} (logical layout scaled and centered)", map[string]any{"W": w, "H": h}))
	for i, s := range st.Screens {
		x, y, bw, bh, ok := rectOfIn(st.Screens, s, size)
		if !ok {
			fmt.Printf("%2d  %-9s %s\n", i+1, s.Connector, tr("cli.dump.tooSmall", "canvas too small, not drawn"))
			continue
		}
		fmt.Printf("%2d  %-9s rect x=%6.1f y=%6.1f w=%6.1f h=%6.1f  center=(%6.1f,%6.1f)  %s\n",
			i+1, s.Connector, x, y, bw, bh, x+bw/2, y+bh/2,
			map[bool]string{true: tr("cli.selftest.resettable", "resettable"), false: "-"}[s.Resettable])
	}
	return nil
}

// runRender 用 Fyne 的软件渲染器（fyne.io/fyne/v2/test）把排列图离线渲染成 PNG，
// 用于在没有人工看屏幕的情况下核对「字形是否真的画出来了」。
func runRender(out, sizeSpec, fontPath string) error {
	st, err := LoadLayout()
	if err != nil {
		return err
	}
	if inv, ierr := Scan(); ierr == nil {
		Decorate(inv, st)
	}
	var w, h int
	if _, err := fmt.Sscanf(sizeSpec, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return fmt.Errorf("bad render size %q, example: -render-size 900x300", sizeSpec)
	}

	a := test.NewApp()
	defer a.Quit()
	if !applyFont(a, fontPath) && isChineseLocale() {
		fmt.Fprintln(os.Stderr, tr("cli.font.warn", "warning: font injection failed; using Fyne built-in font (CJK renders as boxes)"))
	}
	arr := NewArrangement(nil)
	arr.SetScreens(st.Screens)
	w2 := test.NewWindow(arr)
	defer w2.Close()
	w2.Resize(fyne.NewSize(float32(w), float32(h)))
	arr.Resize(fyne.NewSize(float32(w), float32(h)))
	arr.Refresh()

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, w2.Canvas().Capture())
}

// pickCJKFont 挑一个能显示中文的字体文件（Fyne 只吃 ttf/otf，不吃 ttc）。
// 优先走 fontconfig（不同发行版字体路径五花八门），再回退到几个常见路径。
func pickCJKFont() string {
	if p := fontFromFontconfig(); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	cands := []string{
		filepath.Join(home, ".fonts", "simhei.ttf"),
		filepath.Join(home, ".fonts", "MiSans VF.ttf"),
		"/usr/share/fonts/TTF/SimHei.ttf",
		"/usr/share/fonts/MapleMonoNormal-NF-CN/MapleMonoNormalNF-CN-Regular.ttf",
		"/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf",
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			return c
		}
	}
	return ""
}

// fontFromFontconfig 用 fc-list 找注册在 fontconfig 里的中文字体（只要 ttf/otf）。
func fontFromFontconfig() string {
	if _, err := exec.LookPath("fc-list"); err != nil {
		return ""
	}
	out, err := exec.Command("fc-list", "-f", "%{file}\n", ":lang=zh").Output()
	if err != nil {
		return ""
	}
	var cands []string
	for _, line := range strings.Split(string(out), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".ttf", ".otf":
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				cands = append(cands, p)
			}
		}
	}
	if len(cands) == 0 {
		return ""
	}
	// 优先无衬线/黑体类（雅黑、思源黑、MiSans、SimHei…），避免随手挑到仿宋/楷体。
	sort.SliceStable(cands, func(i, j int) bool {
		si, sj := fontScore(cands[i]), fontScore(cands[j])
		if si != sj {
			return si > sj
		}
		return cands[i] < cands[j]
	})
	return cands[0]
}

// fontScore 给字体文件打分：越像「常规无衬线中文字体」分越高。
func fontScore(path string) int {
	b := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(b, "simhei"), strings.Contains(b, "misans"),
		strings.Contains(b, "notosans"), strings.Contains(b, "sourcehansans"),
		strings.Contains(b, "sans"):
		return 3
	case strings.Contains(b, "hei"), strings.Contains(b, "yahei"), strings.Contains(b, "wqy"):
		return 2
	default:
		return 1
	}
}

// hasInternalPanel 判断这块卡上有没有内屏接口（eDP/LVDS/DSI），用于提示「内置 GPU」。
func hasInternalPanel(inv *Inventory, card string) bool {
	for _, c := range inv.Connectors {
		if c.Card != card {
			continue
		}
		for _, p := range []string{"eDP-", "LVDS-", "DSI-"} {
			if strings.HasPrefix(c.Name, p) {
				return true
			}
		}
	}
	return false
}

// orDash 把空字符串显示成 "-"，让 CLI 表格对齐。
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
