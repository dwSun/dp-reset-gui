package main

// 排列图几何与布局签名/翻译包的纯逻辑测试（不需要图形环境）。

import (
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"fyne.io/fyne/v2"
)

func testScreens() []Screen {
	return []Screen{
		{Connector: "DP-4", Card: "card0", X: 0, Y: 0, LogicalW: 1920, LogicalH: 1080, Transform: 0, Resettable: true},
		{Connector: "DP-5", Card: "card0", X: 1920, Y: 0, LogicalW: 1080, LogicalH: 1920, Transform: 1, Resettable: true},
		{Connector: "eDP-1", Card: "card1", X: 0, Y: 1080, LogicalW: 1920, LogicalH: 1080, Transform: 0},
	}
}

func TestRectOfInScalesAndCenters(t *testing.T) {
	screens := testScreens()
	size := fyne.NewSize(1000, 300)
	minX, minY, maxX, maxY := boundsOf(screens)
	if minX != 0 || minY != 0 || maxX != 3000 || maxY != 2160 {
		t.Fatalf("boundsOf = (%v,%v,%v,%v), want (0,0,3000,2160)", minX, minY, maxX, maxY)
	}

	for _, s := range screens {
		x, y, w, h, ok := rectOfIn(screens, s, size)
		if !ok {
			t.Fatalf("%s: rectOfIn returned !ok", s.Connector)
		}
		if x < 0 || y < 0 || x+w > size.Width+0.5 || y+h > size.Height+0.5 {
			t.Errorf("%s rect (%.1f,%.1f,%.1f,%.1f) outside canvas %v", s.Connector, x, y, w, h, size)
		}
	}

	// 同一行上的两块屏应当水平排列、互不重叠。
	x1, _, w1, _, _ := rectOfIn(screens, screens[0], size)
	x2, _, w2, _, _ := rectOfIn(screens, screens[1], size)
	if x1+w1 > x2+1 {
		t.Errorf("screen 0 (x=%.1f w=%.1f) overlaps screen 1 (x=%.1f w=%.1f)", x1, w1, x2, w2)
	}
}

func TestRectOfInTooSmall(t *testing.T) {
	if _, _, _, _, ok := rectOfIn(testScreens(), testScreens()[0], fyne.NewSize(10, 10)); ok {
		t.Fatal("tiny canvas should report !ok")
	}
}

func TestSignatureIgnoresSerialAndIsSorted(t *testing.T) {
	a := &State{LayoutOK: true, Serial: 1, Screens: testScreens()}
	b := &State{LayoutOK: true, Serial: 99, Screens: append([]Screen{}, testScreens()...)}
	// 交换顺序不应改变签名（签名内部排序）。
	b.Screens[0], b.Screens[1] = b.Screens[1], b.Screens[0]
	if a.Signature() != b.Signature() {
		t.Errorf("signature differs on order/serial change:\n%q\n%q", a.Signature(), b.Signature())
	}

	c := &State{LayoutOK: true, Screens: append([]Screen{}, testScreens()...)}
	c.Screens[0].X = 100
	if a.Signature() == c.Signature() {
		t.Error("signature should change when a screen moves")
	}

	// 没有布局后端时签名为空（不做前后比对）。
	if got := (&State{LayoutOK: false}).Signature(); got != "" {
		t.Errorf("hidden layout signature = %q, want empty", got)
	}
}

func TestScreenDeviceID(t *testing.T) {
	s := Screen{Card: "card2", Connector: "DP-7"}
	if got := s.DeviceID(); got != "card2-DP-7" {
		t.Errorf("DeviceID = %q", got)
	}
	if got := (Screen{Connector: "DP-7"}).DeviceID(); got != "DP-7" {
		t.Errorf("DeviceID without card = %q", got)
	}
}

var placeholderRe = regexp.MustCompile(`\{\{\.[A-Za-z0-9_]+\}\}`)

// TestTranslationBundlesConsistent 防止 en/zh 两份翻译包漂移（少了 key 或占位符对不上）。
func TestTranslationBundlesConsistent(t *testing.T) {
	load := func(name string) map[string]string {
		data, err := translationsFS.ReadFile("translations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return m
	}
	en, zh := load("en.json"), load("zh_CN.json")
	if len(en) == 0 {
		t.Fatal("en.json is empty")
	}
	if len(en) != len(zh) {
		t.Errorf("key count differs: en=%d zh=%d", len(en), len(zh))
	}
	for k, v := range en {
		zv, ok := zh[k]
		if !ok {
			t.Errorf("zh_CN.json missing key %q", k)
			continue
		}
		a := placeholderRe.FindAllString(v, -1)
		b := placeholderRe.FindAllString(zv, -1)
		if len(a) != len(b) {
			t.Errorf("key %q placeholder mismatch: en=%v zh=%v", k, a, b)
			continue
		}
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("key %q placeholder #%d: en=%s zh=%s", k, i, a[i], b[i])
			}
		}
	}
	for k := range zh {
		if _, ok := en[k]; !ok {
			t.Errorf("en.json missing key %q", k)
		}
	}
}

func TestDiffSignatures(t *testing.T) {
	before := &State{LayoutOK: true, Screens: testScreens()}
	after := &State{LayoutOK: true, Screens: testScreens()}
	after.Screens[0].X = 200
	diff := diffSignatures(before, after)
	if diff == "" {
		t.Fatal("expected a diff when a screen moved")
	}
	if diffSignatures(before, before) != "" {
		t.Error("identical states should produce empty diff")
	}
}

func TestFallbackStateFromInventory(t *testing.T) {
	inv := &Inventory{Connectors: []Connector{
		{Card: "card0", Name: "DP-1", Device: "card0-DP-1", Status: "connected", GPU: GPU{Driver: "amdgpu"}},
		{Card: "card0", Name: "HDMI-A-1", Device: "card0-HDMI-A-1", Status: "connected", GPU: GPU{Driver: "amdgpu"}},
		{Card: "card1", Name: "DP-2", Device: "card1-DP-2", Status: "connected", GPU: GPU{Driver: "i915"}},
	}}
	st := FallbackState(inv, errors.New("no mutter"))
	if st.LayoutOK {
		t.Fatal("fallback state must not claim a working layout backend")
	}
	if len(st.Screens) != 1 || st.Screens[0].Connector != "DP-1" {
		t.Fatalf("fallback screens = %+v, want only DP-1", st.Screens)
	}
	if !st.Screens[0].Resettable {
		t.Error("fallback screens should be marked resettable")
	}
	if st.Err == "" {
		t.Error("fallback state should carry the layout error for the log")
	}
}
