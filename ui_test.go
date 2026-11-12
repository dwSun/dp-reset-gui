package main

// 界面装配的冒烟测试：用 Fyne 的 test app（软件渲染、无需显示器）
// 真正构造一遍窗口与控件，能抓到 widget 装配/回调/i18n 层面的错误。

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
)

func TestUIBuildsHeadless(t *testing.T) {
	loadTranslations()
	a := test.NewApp()
	defer a.Quit()

	u := newUI(a, ResetOptions{}, "")
	if u.win == nil || u.arr == nil || u.checksBox == nil || u.status == nil {
		t.Fatal("newUI did not build the expected widgets")
	}
	if u.win.Title() == "" {
		t.Error("window title is empty")
	}
	if u.status.Text == "" {
		t.Error("status line should be filled after the initial reload")
	}
	// 排列图/勾选列表应与探测结果保持一致（本机没有 amdgpu 时允许为空列表）。
	if got, want := len(u.checks), len(u.state.Resettable()); got != want {
		t.Errorf("checkboxes = %d, resettable screens = %d", got, want)
	}

	// 选一块可重抽的屏 → 勾选状态与排列图选中集合应同步。
	for id := range u.checks {
		u.arr.SetSelected(id, true)
		u.arr.Refresh()
		if !u.arr.selected[id] {
			t.Errorf("arrangement should remember selection %q", id)
		}
		if got := u.arr.Selected(); len(got) != 1 || got[0] != id {
			t.Errorf("Arrangement.Selected() = %v, want [%s]", got, id)
		}
		break
	}

	// 英文兜底路径（不加载翻译时 tr 的 fallback 也必须可用）。
	if got := tr("no.such.key", "Fallback text"); !strings.Contains(got, "Fallback") {
		t.Errorf("tr fallback = %q, want English fallback", got)
	}
}
