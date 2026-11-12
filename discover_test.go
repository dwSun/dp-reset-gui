package main

// discover.go 的单元测试：用假 sysfs 目录树验证「自动发现」的分类逻辑，
// 不依赖本机真实的显卡/接口。

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSysfs 在临时目录里搭一棵最小的 sysfs 树。
type fakeSysfs struct {
	t    *testing.T
	root string
}

func newFakeSysfs(t *testing.T) *fakeSysfs {
	t.Helper()
	return &fakeSysfs{t: t, root: t.TempDir()}
}

func (f *fakeSysfs) mkdir(rel string) string {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

func (f *fakeSysfs) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", p, err)
	}
}

func (f *fakeSysfs) symlink(target, rel string) {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatalf("symlink %s -> %s: %v", rel, target, err)
	}
}

// addGPU 添加一块 PCI 显卡：drivers/<drv>、devices/.../<bdf>、class/drm/<card>。
func (f *fakeSysfs) addGPU(card, bdf, driver, pciIDs string, removable bool) {
	f.t.Helper()
	devRel := "devices/pci0000:00/0000:00:01.0/" + bdf
	f.mkdir(devRel)
	f.mkdir("bus/pci/drivers/" + driver)
	f.symlink("../../../../bus/pci/drivers/"+driver, devRel+"/driver")
	f.write(devRel+"/uevent", "DRIVER="+driver+"\nPCI_ID="+pciIDs+"\n")

	f.mkdir("class/drm/" + card)
	f.symlink("../../../"+devRel, "class/drm/"+card+"/device")

	f.mkdir("bus/pci/devices/" + bdf)
	rm := "fixed"
	if removable {
		rm = "removable"
	}
	f.write("bus/pci/devices/"+bdf+"/removable", rm+"\n")
}

func (f *fakeSysfs) addConnector(card, name, status, enabled, dpms string) {
	f.t.Helper()
	rel := "class/drm/" + card + "-" + name
	f.mkdir(rel)
	f.write(rel+"/status", status+"\n")
	f.write(rel+"/enabled", enabled+"\n")
	f.write(rel+"/dpms", dpms+"\n")
}

func TestScanSysfsClassification(t *testing.T) {
	f := newFakeSysfs(t)
	f.addGPU("card0", "0000:01:00.0", "amdgpu", "1002:67DF", true)
	f.addGPU("card1", "0000:00:02.0", "i915", "8086:7D55", false)

	f.addConnector("card0", "DP-4", "connected", "enabled", "On")
	f.addConnector("card0", "DP-5", "disconnected", "disabled", "Off")
	f.addConnector("card0", "HDMI-A-1", "connected", "enabled", "On")
	f.addConnector("card1", "DP-1", "connected", "enabled", "On")
	f.addConnector("card1", "eDP-1", "connected", "enabled", "On")

	inv, err := scanSysfs(f.root)
	if err != nil {
		t.Fatalf("scanSysfs: %v", err)
	}
	if len(inv.GPUs) != 2 {
		t.Fatalf("GPUs = %d, want 2", len(inv.GPUs))
	}
	if inv.GPUs[0].Card != "card0" || inv.GPUs[0].BDF != "0000:01:00.0" || inv.GPUs[0].Driver != "amdgpu" {
		t.Errorf("GPU[0] = %+v", inv.GPUs[0])
	}
	if !inv.GPUs[0].Hotplug {
		t.Errorf("card0 should be detected as hot-pluggable")
	}
	if inv.GPUs[0].Vendor != "0x1002" || inv.GPUs[0].Device != "0x67df" {
		t.Errorf("PCI ids = %s/%s, want 0x1002/0x67df", inv.GPUs[0].Vendor, inv.GPUs[0].Device)
	}

	res := inv.Resettable()
	if len(res) != 1 {
		t.Fatalf("Resettable() = %v, want exactly [card0-DP-4]", res)
	}
	if res[0].Device != "card0-DP-4" {
		t.Errorf("resettable connector = %q, want card0-DP-4", res[0].Device)
	}

	// HDMI / eDP / 未连接 / 非 amdgpu 的 DP 都不能重抽。
	for _, c := range inv.Connectors {
		if c.Device == "card0-DP-4" {
			continue
		}
		if c.Resettable() {
			t.Errorf("%s should not be resettable", c.Device)
		}
	}

	if c, ok := inv.Lookup("card0", "DP-5"); !ok || c.Status != "disconnected" {
		t.Errorf("Lookup(card0, DP-5) = %+v, %v", c, ok)
	}
}

func TestScanSysfsNoDRM(t *testing.T) {
	f := newFakeSysfs(t)
	if _, err := scanSysfs(f.root); err == nil {
		t.Fatal("scanSysfs on empty tree should fail")
	}
}

func TestScanSysfsDPNameMatching(t *testing.T) {
	f := newFakeSysfs(t)
	f.addGPU("card0", "0000:05:00.0", "amdgpu", "1002:67DF", false)
	f.addConnector("card0", "DP-4", "connected", "enabled", "On")
	// MST 子接口（名字里还有一段）不是 DP-N，不参与重抽。
	f.addConnector("card0", "DP-4-1", "connected", "enabled", "On")

	inv, err := scanSysfs(f.root)
	if err != nil {
		t.Fatalf("scanSysfs: %v", err)
	}
	res := inv.Resettable()
	if len(res) != 1 || res[0].Name != "DP-4" {
		t.Fatalf("Resettable() = %+v, want only DP-4", res)
	}
}

func TestSplitConnectorID(t *testing.T) {
	cases := []struct {
		in   string
		card string
		name string
		ok   bool
	}{
		{"card0-DP-4", "card0", "DP-4", true},
		{"card12-DP-9", "card12", "DP-9", true},
		{"DP-4", "", "", false},
		{"card0-HDMI-A-1", "", "", false},
		{"card0-eDP-1", "", "", false},
		{"card0-DP-4-1", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		card, name, ok := splitConnectorID(c.in)
		if ok != c.ok || card != c.card || name != c.name {
			t.Errorf("splitConnectorID(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, card, name, ok, c.card, c.name, c.ok)
		}
	}
}
