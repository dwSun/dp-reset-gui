package main

// 设备探测：从 sysfs 里找出「哪块卡是 amdgpu」「哪个接口是可重抽的 DP 输出」。
//
// 早先的版本把 PCI 地址和接口白名单写死在 root 助手和 Python 里，换台机器就废。
// 这里改成全自动：
//
//   卡      = /sys/class/drm/cardN 里、driver 为 amdgpu 的那些
//   PCI 地址 = cardN/device 这个符号链接解析出来的 PCI BDF
//   可重抽  = 该卡上的 DP-N（排除 eDP / HDMI / MST 子接口）、且 status=connected
//
// 只读 sysfs（普通用户即可），不需要 root；root 助手会再做一次独立校验。
// 所有路径都通过 sysRoot 注入，方便单测用假目录树。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	cardRe   = regexp.MustCompile(`^card[0-9]+$`)
	dpNameRe = regexp.MustCompile(`^DP-[0-9]+$`)
	bdfRe    = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-7]$`)
)

// GPU 是一块 DRM 显卡（对应 /sys/class/drm/cardN）。
type GPU struct {
	Card    string // card0
	BDF     string // 例如 0000:01:00.0（非 PCI 设备为空）
	Driver  string // amdgpu / i915 / nouveau ...
	Vendor  string // 0x1002
	Device  string // 0x67df
	Hotplug bool   // 可热插拔（多见于 Thunderbolt/USB4 外置显卡坞）
}

// IsAMDGPU 表示这个端口的 debugfs 里有 trigger_hotplug 可写。
func (g GPU) IsAMDGPU() bool { return g.Driver == "amdgpu" }

// Connector 是一个 DRM 输出接口（对应 /sys/class/drm/cardN-NAME）。
type Connector struct {
	Card    string // card0
	Name    string // DP-4
	Device  string // card0-DP-4，传给 root 助手的唯一标识
	GPU     GPU
	Status  string // connected / disconnected
	Enabled string // enabled / disabled
	DPMS    string // On / Off / ...
}

// Resettable 判断这个接口能不能做 DP 链路重抽：
// 必须是 amdgpu 上的、名字是 DP-N 的、当前 connected 的接口。
// 内屏（eDP）、HDMI、MST 子接口（DP-1-1）都不在此列。
func (c Connector) Resettable() bool {
	return c.GPU.Driver == "amdgpu" && dpNameRe.MatchString(c.Name) && c.Status == "connected"
}

// Inventory 是一次 sysfs 扫描的完整结果。
type Inventory struct {
	GPUs       []GPU
	Connectors []Connector
}

// Resettable 返回可重抽的接口（按卡号、接口号排序）。
func (inv *Inventory) Resettable() []Connector {
	out := make([]Connector, 0, len(inv.Connectors))
	for _, c := range inv.Connectors {
		if c.Resettable() {
			out = append(out, c)
		}
	}
	return out
}

// Lookup 按 cardN-NAME 找接口。
func (inv *Inventory) Lookup(card, name string) (Connector, bool) {
	for _, c := range inv.Connectors {
		if c.Card == card && c.Name == name {
			return c, true
		}
	}
	return Connector{}, false
}

// Scan 扫描真实系统（/sys）。
func Scan() (*Inventory, error) { return scanSysfs("/sys") }

// scanSysfs 扫描给定的 sysfs 根目录（单测传假目录）。
func scanSysfs(sysRoot string) (*Inventory, error) {
	drmRoot := filepath.Join(sysRoot, "class", "drm")
	entries, err := os.ReadDir(drmRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", drmRoot, err)
	}

	inv := &Inventory{}
	cards := make(map[string]GPU, 8)

	// 第一遍：先把所有 cardN 解析成 GPU。
	for _, e := range entries {
		name := e.Name()
		if !cardRe.MatchString(name) {
			continue
		}
		devDir := filepath.Join(drmRoot, name, "device")
		g := GPU{Card: name}
		if real, rerr := filepath.EvalSymlinks(devDir); rerr == nil {
			if b := filepath.Base(real); bdfRe.MatchString(b) {
				g.BDF = b
			}
		}
		if real, rerr := filepath.EvalSymlinks(filepath.Join(devDir, "driver")); rerr == nil {
			g.Driver = filepath.Base(real)
		}
		g.Vendor, g.Device = readPCIIDs(filepath.Join(devDir, "uevent"))
		if g.BDF != "" {
			g.Hotplug = readTrim(filepath.Join(sysRoot, "bus", "pci", "devices", g.BDF, "removable")) == "removable"
		}
		cards[name] = g
		inv.GPUs = append(inv.GPUs, g)
	}

	// 第二遍：解析 cardN-NAME 形式的接口目录。
	for _, e := range entries {
		full := e.Name() // 例如 card0-DP-4
		dash := strings.IndexByte(full, '-')
		if dash <= 0 {
			continue
		}
		card, connName := full[:dash], full[dash+1:]
		g, ok := cards[card]
		if !ok || connName == "" {
			continue
		}
		dir := filepath.Join(drmRoot, full)
		if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
			continue
		}
		inv.Connectors = append(inv.Connectors, Connector{
			Card:    card,
			Name:    connName,
			Device:  full,
			GPU:     g,
			Status:  readTrim(filepath.Join(dir, "status")),
			Enabled: readTrim(filepath.Join(dir, "enabled")),
			DPMS:    readTrim(filepath.Join(dir, "dpms")),
		})
	}

	sort.Slice(inv.GPUs, func(i, j int) bool { return cardIndex(inv.GPUs[i].Card) < cardIndex(inv.GPUs[j].Card) })
	sort.SliceStable(inv.Connectors, func(i, j int) bool {
		a, b := inv.Connectors[i], inv.Connectors[j]
		if ca, cb := cardIndex(a.Card), cardIndex(b.Card); ca != cb {
			return ca < cb
		}
		na, nb := connSortKey(a.Name), connSortKey(b.Name)
		if na.kind != nb.kind {
			return na.kind < nb.kind
		}
		if na.num != nb.num {
			return na.num < nb.num
		}
		return na.text < nb.text
	})
	if len(inv.GPUs) == 0 {
		return inv, fmt.Errorf("no DRM card found under %s", drmRoot)
	}
	return inv, nil
}

type sortKey struct {
	kind int // 0 = DP-N（按数字），1 = 其它（按字母）
	num  int
	text string
}

func connSortKey(name string) sortKey {
	if m := dpNameRe.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(strings.TrimPrefix(name, "DP-"))
		return sortKey{kind: 0, num: n}
	}
	return sortKey{kind: 1, text: name}
}

func cardIndex(card string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(card, "card"))
	return n
}

// readPCIIDs 从 PCI 设备的 uevent 里取 vendor/device id（如 0x1002 / 0x67df）。
func readPCIIDs(ueventPath string) (vendor, device string) {
	for _, line := range strings.Split(readTrim(ueventPath), "\n") {
		v, ok := strings.CutPrefix(line, "PCI_ID=")
		if !ok {
			continue
		}
		vs, ds, found := strings.Cut(v, ":")
		if !found {
			continue
		}
		return "0x" + strings.ToLower(vs), "0x" + strings.ToLower(ds)
	}
	return "", ""
}

// readTrim 读文件并去掉首尾空白；读不到返回空串（缺失属性不该让整次探测失败）。
func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
