package main

// 布局数据层：桌面布局（GNOME/mutter 后端）+ 内嵌助手脚本的落地。
//
// 说明：
//   - 布局数据只有 GNOME/mutter 提供（org.gnome.Mutter.DisplayConfig）。
//     其它桌面环境下 LoadLayout 会失败，UI 自动降级成「只列接口」模式。
//   - Python 助手和 root 助手在编译时 //go:embed 进二进制，运行时释放到
//     $XDG_DATA_HOME/dp-reset-gui/（默认 ~/.local/share/dp-reset-gui/）。
//     这样单个二进制即可运行，不需要随包分发脚本。

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed assets/sys_layout.py assets/dp-reset-root
var assetsFS embed.FS

// Screen 是一块正在使用的输出（逻辑屏里可能有多块 = 镜像）。
type Screen struct {
	Connector  string  `json:"connector"`
	Product    string  `json:"product"`
	Builtin    bool    `json:"builtin"`
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Scale      float64 `json:"scale"`
	Transform  int     `json:"transform"` // 0=正常 1=90° 2=180° 3=270°
	Primary    bool    `json:"primary"`
	PxW        int     `json:"px_w"`
	PxH        int     `json:"px_h"`
	LogicalW   float64 `json:"logical_w"`
	LogicalH   float64 `json:"logical_h"`
	Mode       string  `json:"mode"`
	Refresh    float64 `json:"refresh"`
	Card       string  `json:"card"`
	Status     string  `json:"status"`
	Enabled    string  `json:"enabled"`
	DPMS       string  `json:"dpms"`
	Resettable bool    `json:"-"` // 由 Go 侧 discover 结果填充，不信任 Python
}

// State 是一次布局快照。LayoutOK=false 表示当前桌面不提供布局信息
// （非 GNOME/mutter），此时 Screens 由 sysfs 探测结果合成，仅用于列表选择。
type State struct {
	Serial     int      `json:"serial"`
	LayoutMode int      `json:"layout_mode"`
	Screens    []Screen `json:"screens"`
	Err        string   `json:"error"`

	LayoutOK bool   `json:"-"`
	Source   string `json:"-"` // "mutter" / "sysfs"
}

// Signature 只包含「布局本身」的信息，用来判断 reset 是否把配置打乱。
// 刻意不含 mutter serial —— 每次热插拔都会 +1，那不是布局变化。
func (s *State) Signature() string {
	if s == nil || !s.LayoutOK {
		return ""
	}
	parts := make([]string, 0, len(s.Screens))
	for _, sc := range s.Screens {
		parts = append(parts, fmt.Sprintf("%s@%d,%d %.0fx%.0f r%d", sc.Connector, sc.X, sc.Y, sc.LogicalW, sc.LogicalH, sc.Transform))
	}
	sort.Strings(parts)
	return strings.Join(parts, " | ")
}

// Resettable 返回可重抽的屏（按 sysfs 判定结果）。
func (s *State) Resettable() []Screen {
	if s == nil {
		return nil
	}
	out := make([]Screen, 0, len(s.Screens))
	for _, sc := range s.Screens {
		if sc.Resettable {
			out = append(out, sc)
		}
	}
	return out
}

// DeviceID 是接口的唯一标识（传给 root 助手的参数，形如 card0-DP-4）。
func (s Screen) DeviceID() string {
	if s.Card == "" {
		return s.Connector
	}
	return s.Card + "-" + s.Connector
}

// rotationLabel 返回旋转角度的可读文案（无旋转时走 i18n）。
func rotationLabel(transform int) string {
	switch transform {
	case 1:
		return "90°"
	case 2:
		return "180°"
	case 3:
		return "270°"
	default:
		return tr("rotation.normal", "Normal")
	}
}

// rotationCompact 是 CLI 表格里用的紧凑旋转值（纯 ASCII，方便列对齐）。
func rotationCompact(transform int) string {
	switch transform {
	case 1:
		return "90°"
	case 2:
		return "180°"
	case 3:
		return "270°"
	default:
		return "0°"
	}
}

// userDataDir 返回本工具存放运行时助手脚本的目录（遵循 XDG_DATA_HOME）。
func userDataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "dp-reset-gui"), nil
}

// monitorsXMLPath 返回 GNOME 的显示器配置路径（遵循 XDG_CONFIG_HOME）。
func monitorsXMLPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, "monitors.xml")
}

// pythonBin 找到能跑 gi 的 python3（系统解释器，不走 venv）。
func pythonBin() (string, error) {
	if p, err := exec.LookPath("python3"); err == nil {
		return p, nil
	}
	if st, err := os.Stat("/usr/bin/python3"); err == nil && st.Mode().IsRegular() {
		return "/usr/bin/python3", nil
	}
	return "", errors.New("python3 not found (needs python3-gi / python-gobject)")
}

// ensureAssets 把内嵌脚本释放到用户目录，返回 (布局助手, root 助手) 的绝对路径。
// 内容变了就重写，保证与二进制版本一致。只读自检（-selftest/-dump/-render）不依赖
// 它——布局助手直接从嵌入内容经 stdin 喂给 python，不落盘。
func ensureAssets() (string, string, error) {
	dir, err := userDataDir()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	pyPath := filepath.Join(dir, "sys_layout.py")
	helperPath := filepath.Join(dir, "dp-reset-root")
	for _, f := range []struct {
		src, dst string
		mode     os.FileMode
	}{
		{"assets/sys_layout.py", pyPath, 0o644},
		{"assets/dp-reset-root", helperPath, 0o755},
	} {
		want, rerr := assetsFS.ReadFile(f.src)
		if rerr != nil {
			return "", "", rerr
		}
		cur, rerr := os.ReadFile(f.dst)
		if rerr != nil || string(cur) != string(want) {
			if werr := os.WriteFile(f.dst, want, f.mode); werr != nil {
				return "", "", werr
			}
			// WriteFile 不改已存在文件的权限，这里显式兜一下
			if cerr := os.Chmod(f.dst, f.mode); cerr != nil {
				return "", "", cerr
			}
		}
	}
	return pyPath, helperPath, nil
}

// LoadLayout 调系统 python3（有 gi）读取 mutter 当前布局。
// 脚本从嵌入内容走 stdin 传给 python，不需要先落盘（只读命令因此更干净）。
func LoadLayout() (*State, error) {
	py, err := pythonBin()
	if err != nil {
		return nil, err
	}
	script, err := assetsFS.ReadFile("assets/sys_layout.py")
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-")
	cmd.Stdin = bytes.NewReader(script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errors.New("timeout calling mutter DisplayConfig")
		}
		return nil, fmt.Errorf("python sys_layout.py: %v %s", err, strings.TrimSpace(stderr.String()))
	}

	var st State
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, fmt.Errorf("parse layout JSON: %v (first 200 bytes: %s)", err, head(string(out), 200))
	}
	if st.Err != "" {
		return nil, errors.New(st.Err)
	}
	st.LayoutOK = true
	st.Source = "mutter"
	return &st, nil
}

// Decorate 用 sysfs 探测结果标记每块屏是否可重抽（布局后端只提供几何，不提供能力）。
func Decorate(inv *Inventory, st *State) *State {
	if st == nil {
		return nil
	}
	for i := range st.Screens {
		sc := &st.Screens[i]
		if sc.Card == "" {
			// 老版本 Python 助手不返回 card；按接口名在探测结果里补回来。
			for _, c := range inv.Connectors {
				if c.Name == sc.Connector {
					sc.Card = c.Card
					break
				}
			}
		}
		c, ok := inv.Lookup(sc.Card, sc.Connector)
		if !ok {
			continue
		}
		sc.Resettable = c.Resettable()
		sc.Status, sc.Enabled, sc.DPMS = c.Status, c.Enabled, c.DPMS
	}
	return st
}

// FallbackState 在布局后端不可用时，用 sysfs 探测结果合成一个「仅接口」状态。
func FallbackState(inv *Inventory, cause error) *State {
	st := &State{LayoutOK: false, Source: "sysfs"}
	if cause != nil {
		st.Err = cause.Error()
	}
	for _, c := range inv.Resettable() {
		st.Screens = append(st.Screens, Screen{
			Connector:  c.Name,
			Card:       c.Card,
			Status:     c.Status,
			Enabled:    c.Enabled,
			DPMS:       c.DPMS,
			Resettable: true,
		})
	}
	return st
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
