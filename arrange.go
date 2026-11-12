package main

// 排列图控件：按 mutter 的逻辑坐标等比绘制各块屏，每块标上编号/接口/分辨率/旋转。
// 点击某块 = 切换它的选中状态（与下方多选控件联动）。
// 没有布局数据时（非 GNOME 桌面）显示一行提示，不画方块。

import (
	"fmt"
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

var (
	colBG        = color.NRGBA{R: 22, G: 24, B: 28, A: 255}
	colAvailable = color.NRGBA{R: 52, G: 84, B: 128, A: 255}  // 可重抽
	colSelected  = color.NRGBA{R: 214, G: 128, B: 40, A: 255} // 已选中
	colLocked    = color.NRGBA{R: 58, G: 58, B: 62, A: 255}   // 不可重抽（内屏 / HDMI / 未连接 / 非 amdgpu）
	colText      = color.NRGBA{R: 244, G: 244, B: 246, A: 255}
	colTextDim   = color.NRGBA{R: 150, G: 150, B: 156, A: 255}
)

// Arrangement 是可点击的排列图。
type Arrangement struct {
	widget.BaseWidget
	screens  []Screen
	selected map[string]bool // key = Screen.DeviceID()（cardN-DP-N）
	noLayout bool
	onToggle func(device string)
}

// NewArrangement 构造一个空排列图。
func NewArrangement(onToggle func(string)) *Arrangement {
	a := &Arrangement{selected: map[string]bool{}, onToggle: onToggle}
	a.ExtendBaseWidget(a)
	return a
}

// SetScreens 更新要绘制的屏幕列表（保留仍有效的选中项）。
func (a *Arrangement) SetScreens(screens []Screen) {
	valid := make(map[string]bool, len(screens))
	for _, s := range screens {
		if s.Resettable {
			valid[s.DeviceID()] = true
		}
	}
	for dev := range a.selected {
		if !valid[dev] {
			delete(a.selected, dev)
		}
	}
	a.screens = screens
	a.Refresh()
}

// SetNoLayout 控制是否进入「无布局」模式（只显示提示文字）。
func (a *Arrangement) SetNoLayout(v bool) {
	a.noLayout = v
	a.Refresh()
}

// SetSelected 由外部（多选控件）同步选中状态。
func (a *Arrangement) SetSelected(device string, on bool) {
	if on {
		a.selected[device] = true
	} else {
		delete(a.selected, device)
	}
	a.Refresh()
}

// Selected 返回当前选中的接口 ID（按视觉顺序）。
func (a *Arrangement) Selected() []string {
	out := make([]string, 0, len(a.selected))
	for _, s := range a.screens {
		if id := s.DeviceID(); a.selected[id] {
			out = append(out, id)
		}
	}
	return out
}

// Tapped 命中哪块就切换哪块。
func (a *Arrangement) Tapped(e *fyne.PointEvent) {
	if a.noLayout {
		return
	}
	for _, s := range a.screens {
		if !s.Resettable {
			continue
		}
		x, y, w, h, ok := rectOfIn(a.screens, s, a.Size())
		if !ok {
			continue
		}
		if e.Position.X >= x && e.Position.X <= x+w &&
			e.Position.Y >= y && e.Position.Y <= y+h {
			dev := s.DeviceID()
			on := !a.selected[dev]
			a.SetSelected(dev, on)
			if a.onToggle != nil {
				a.onToggle(dev)
			}
			return
		}
	}
}

// CreateRenderer 实现 fyne.Widget。
func (a *Arrangement) CreateRenderer() fyne.WidgetRenderer {
	return &arrRenderer{w: a, bg: canvas.NewRectangle(colBG)}
}

// boundsOf 返回所有屏的包围盒（逻辑坐标）。
func boundsOf(screens []Screen) (minX, minY, maxX, maxY float64) {
	first := true
	for _, s := range screens {
		if first {
			minX, minY, maxX, maxY = float64(s.X), float64(s.Y), float64(s.X)+s.LogicalW, float64(s.Y)+s.LogicalH
			first = false
			continue
		}
		minX = math.Min(minX, float64(s.X))
		minY = math.Min(minY, float64(s.Y))
		maxX = math.Max(maxX, float64(s.X)+s.LogicalW)
		maxY = math.Max(maxY, float64(s.Y)+s.LogicalH)
	}
	return
}

// rectOfIn 计算某块屏在控件内的矩形（控件内坐标，单位像素）：等比缩放 + 居中。
// 纯几何计算（不依赖控件实例），因此可以在无 GUI 的 -dump 自检里复用。
func rectOfIn(all []Screen, s Screen, size fyne.Size) (x, y, w, h float32, ok bool) {
	minX, minY, maxX, maxY := boundsOf(all)
	bw, bh := maxX-minX, maxY-minY
	if bw <= 0 || bh <= 0 || size.Width < 20 || size.Height < 20 {
		return 0, 0, 0, 0, false
	}
	const pad = 10.0
	scale := math.Min((float64(size.Width)-2*pad)/bw, (float64(size.Height)-2*pad)/bh)
	offX := (float64(size.Width) - bw*scale) / 2
	offY := (float64(size.Height) - bh*scale) / 2
	cx := offX + (float64(s.X)-minX)*scale
	cy := offY + (float64(s.Y)-minY)*scale
	cw := math.Max(s.LogicalW*scale, 24)
	ch := math.Max(s.LogicalH*scale, 18)
	return float32(cx), float32(cy), float32(cw), float32(ch), true
}

type arrRenderer struct {
	w      *Arrangement
	bg     *canvas.Rectangle
	msg    *widget.Label
	rects  []*canvas.Rectangle
	text1s []*canvas.Text // 第一行：编号 + 接口
	text2s []*canvas.Text // 第二行：分辨率 + 旋转（方块太小时留空）
}

func (r *arrRenderer) rebuild() {
	if r.msg == nil {
		r.msg = widget.NewLabel("")
		r.msg.Alignment = fyne.TextAlignCenter
		r.msg.Wrapping = fyne.TextWrapWord
		r.msg.TextStyle = fyne.TextStyle{Italic: true}
	}
	n := len(r.w.screens)
	if len(r.rects) == n {
		return
	}
	r.rects = make([]*canvas.Rectangle, n)
	r.text1s = make([]*canvas.Text, n)
	r.text2s = make([]*canvas.Text, n)
	for i := 0; i < n; i++ {
		rc := canvas.NewRectangle(colAvailable)
		rc.StrokeColor = color.NRGBA{R: 200, G: 200, B: 210, A: 255}
		rc.StrokeWidth = 1.5
		mkTxt := func(size float32) *canvas.Text {
			tx := canvas.NewText("", colText)
			tx.TextSize = size
			tx.Alignment = fyne.TextAlignCenter
			return tx
		}
		r.rects[i] = rc
		r.text1s[i] = mkTxt(12)
		r.text2s[i] = mkTxt(11) // 第二行小一号，窄竖屏也放得下
	}
}

func (r *arrRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.bg.Move(fyne.NewPos(0, 0))
	r.rebuild()

	if r.w.noLayout {
		for i := range r.rects {
			r.rects[i].Resize(fyne.NewSize(0, 0))
			r.text1s[i].Text = ""
			r.text2s[i].Text = ""
		}
		r.msg.Text = tr("arrange.noLayout", "No desktop layout available — pick connectors from the list below.")
		r.msg.Resize(fyne.NewSize(size.Width-40, 60))
		r.msg.Move(fyne.NewPos(20, (size.Height-60)/2))
		return
	}
	r.msg.Text = ""
	r.msg.Resize(fyne.NewSize(0, 0))

	for i, s := range r.w.screens {
		rc := r.rects[i]
		x, y, bw, bh, ok := rectOfIn(r.w.screens, s, size)
		if !ok {
			rc.Resize(fyne.NewSize(0, 0))
			r.text1s[i].Text, r.text2s[i].Text = "", ""
			continue
		}
		rc.Move(fyne.NewPos(x, y))
		rc.Resize(fyne.NewSize(bw, bh))
		switch {
		case !s.Resettable:
			rc.FillColor = colLocked
		case r.w.selected[s.DeviceID()]:
			rc.FillColor = colSelected
		default:
			rc.FillColor = colAvailable
		}
		l1, l2 := labelLines(s, i+1, fyne.NewSize(bw, bh))
		t1, t2 := r.text1s[i], r.text2s[i]
		clr := colText
		if !s.Resettable {
			clr = colTextDim
		}
		// 注意：canvas.Text 只画单行——换行符会被当成缺字形画成方块，
		// 所以两行文案必须用两个 canvas.Text 对象分别定位。
		t1.Text, t1.Color = l1, clr
		t1.Resize(t1.MinSize())
		t2.Text, t2.Color = l2, clr
		t2.Resize(t2.MinSize())
		if l2 == "" {
			t1.Move(fyne.NewPos(x+(bw-t1.MinSize().Width)/2, y+(bh-t1.MinSize().Height)/2))
			t2.Move(fyne.NewPos(x, y))
			continue
		}
		th := t1.MinSize().Height + t2.MinSize().Height
		top := y + float32(math.Max(float64((bh-th)/2), 1))
		t1.Move(fyne.NewPos(x+(bw-t1.MinSize().Width)/2, top))
		t2.Move(fyne.NewPos(x+(bw-t2.MinSize().Width)/2, top+t1.MinSize().Height))
	}
}

// labelLines 按可用空间决定画几行（小方块只显示编号，其次只显示编号+接口）。
func labelLines(s Screen, num int, size fyne.Size) (line1, line2 string) {
	if size.Width < 62 {
		return fmt.Sprintf("%d", num), ""
	}
	line1 = fmt.Sprintf("%d %s", num, s.Connector)
	// 第二行用 11px（见 rebuild），实测 "1920x1080 90°" 约需 82px 宽
	if size.Width < 82 || size.Height < 46 {
		return line1, ""
	}
	return line1, fmt.Sprintf("%dx%d %s", s.PxW, s.PxH, rotationLabel(s.Transform))
}

func (r *arrRenderer) MinSize() fyne.Size { return fyne.NewSize(420, 210) }

func (r *arrRenderer) Refresh() {
	if len(r.rects) != len(r.w.screens) {
		r.rects, r.text1s, r.text2s = nil, nil, nil
	}
	r.rebuild()
	r.Layout(r.w.Size())
	canvas.Refresh(r.w)
}

func (r *arrRenderer) Objects() []fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, 0, 2+3*len(r.rects))
	objs = append(objs, r.bg, r.msg)
	for i := range r.rects {
		objs = append(objs, r.rects[i], r.text1s[i], r.text2s[i])
	}
	return objs
}

func (r *arrRenderer) Destroy() {}
