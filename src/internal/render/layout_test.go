package render

import (
	"image"
	"testing"
)

func TestCanvasSize(t *testing.T) {
	if CanvasW != 1448 || CanvasH != 1072 {
		t.Errorf("画布 = %dx%d，必须是 1448x1072", CanvasW, CanvasH)
	}
}

func TestRegionsInsideCanvas(t *testing.T) {
	for _, r := range Regions() {
		if r.Rect.X < 0 || r.Rect.Y < 0 ||
			r.Rect.Right() > CanvasW || r.Rect.Bottom() > CanvasH {
			t.Errorf("区域 %s 越界: %+v", r.ID, r.Rect)
		}
	}
}

func TestRegionsDoNotOverlap(t *testing.T) {
	rs := Regions()
	for i := 0; i < len(rs); i++ {
		for j := i + 1; j < len(rs); j++ {
			if rs[i].Rect.Standard().Overlaps(rs[j].Rect.Standard()) {
				t.Errorf("区域 %s 与 %s 重叠", rs[i].ID, rs[j].ID)
			}
		}
	}
}

func TestRegionsIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Regions() {
		if seen[r.ID] {
			t.Errorf("区域 ID 重复: %s", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestRegionsWaveforms(t *testing.T) {
	want := map[string]string{
		"clock":    "REAGL",
		"date":     "GC16",
		"weather":  "GC16",
		"calendar": "GC16",
		"courses":  "GC16",
		"footer":   "GL16",
	}
	for _, r := range Regions() {
		if w, ok := want[r.ID]; !ok {
			t.Errorf("意外的区域 %s", r.ID)
		} else if r.Waveform != w {
			t.Errorf("区域 %s 波形 = %q，期望 %q", r.ID, r.Waveform, w)
		}
	}
	// 不能有 A2（实测在 PW3 上慢 8 倍）
	for _, r := range Regions() {
		if r.Waveform == "A2" {
			t.Errorf("区域 %s 使用了被禁用的 A2 波形", r.ID)
		}
	}
}

func TestRectStandardMatchesFields(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 30, H: 40}
	if r.Right() != 40 {
		t.Errorf("Right = %d", r.Right())
	}
	if r.Bottom() != 60 {
		t.Errorf("Bottom = %d", r.Bottom())
	}
	if r.Standard() != image.Rect(10, 20, 40, 60) {
		t.Errorf("Standard = %v", r.Standard())
	}
}

// 区域必须覆盖画布上所有会变的内容。漏掉一块 = 那块永远不刷新。
// 这条测试用「区域并集面积」做粗检：右栏从 y=72 起、左栏底部到 footer，
// 中间不应有整条空白带。
func TestRegionsCoverBothColumns(t *testing.T) {
	var leftMaxY, rightMaxY, rightMinY int
	rightMinY = CanvasH
	for _, r := range Regions() {
		if r.Rect.X < DividerX {
			if r.Rect.Bottom() > leftMaxY {
				leftMaxY = r.Rect.Bottom()
			}
		} else {
			if r.Rect.Bottom() > rightMaxY {
				rightMaxY = r.Rect.Bottom()
			}
			if r.Rect.Y < rightMinY {
				rightMinY = r.Rect.Y
			}
		}
	}
	// 右栏必须从月份标题的 y=72 开始，否则跨月刷不新
	if rightMinY > MonthY {
		t.Errorf("右栏区域从 y=%d 起，月份标题在 y=%d，会漏刷", rightMinY, MonthY)
	}
	// 两栏都要覆盖到接近画布底部（留 56px 外边距）
	if leftMaxY < CanvasH-Margin {
		t.Errorf("左栏区域只到 y=%d，画布高 %d，底部有未覆盖带", leftMaxY, CanvasH)
	}
	if rightMaxY < CanvasH-Margin {
		t.Errorf("右栏区域只到 y=%d，画布高 %d，底部有未覆盖带", rightMaxY, CanvasH)
	}
}

func TestClockRegionCoversClockText(t *testing.T) {
	// 时钟区必须完整覆盖 §4.2 里「14:32」的 200px 高数字
	var clock Rect
	for _, r := range Regions() {
		if r.ID == "clock" {
			clock = r.Rect
		}
	}
	if clock.W < 400 || clock.H < 200 {
		t.Errorf("时钟区太小: %+v", clock)
	}
}

// 六个区域的坐标必须逐字钉死。
//
// 只测「在画布内 / 不重叠 / 波形正确」这类结构性不变量是不够的：
// 评审用 23 个变异实验证明，删掉 date 区、删掉 weather 区、
// weather 高度 328→200、courses 顶端 632→640、date 顶端 72→90、
// clock 宽度 544→400 全部能存活 —— 而这六个矩形正是本任务的全部价值。
func TestRegionsMatchExpectedGeometry(t *testing.T) {
	want := map[string]Rect{
		"clock":    {X: 56, Y: 250, W: 544, H: 220},
		"date":     {X: 56, Y: 72, W: 544, H: 178},
		"weather":  {X: 56, Y: 540, W: 544, H: 328},
		"calendar": {X: 682, Y: 72, W: 710, H: 548},
		"courses":  {X: 682, Y: 632, W: 710, H: 440},
		"footer":   {X: 56, Y: 962, W: 544, H: 54},
	}

	got := Regions()
	if len(got) != len(want) {
		t.Fatalf("区域数 = %d，期望 %d", len(got), len(want))
	}
	for _, r := range got {
		w, ok := want[r.ID]
		if !ok {
			t.Errorf("意外区域 %s", r.ID)
			continue
		}
		if r.Rect != w {
			t.Errorf("区域 %s 几何 = %+v，期望 %+v", r.ID, r.Rect, w)
		}
	}
}

// 区域顺序也必须固定：Task 9 的 MapRegions 按索引比对，日志按序输出
func TestRegionsOrderIsStable(t *testing.T) {
	want := []string{"clock", "date", "weather", "calendar", "courses", "footer"}
	got := Regions()
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("第 %d 个区域 = %q，期望 %q", i, got[i].ID, w)
		}
	}
}

// courses 必须一直画到画布底：翻页页码画在 y≈1024..1064
func TestCoursesRegionReachesCanvasBottom(t *testing.T) {
	for _, r := range Regions() {
		if r.ID == "courses" {
			if got := r.Rect.Bottom(); got != CanvasH {
				t.Errorf("courses 底边 = %d，必须是 %d", got, CanvasH)
			}
			return
		}
	}
	t.Fatal("找不到 courses 区域")
}

// calendar 必须从月份标题所在的 y=72 起
func TestCalendarRegionStartsAtMonthTitle(t *testing.T) {
	for _, r := range Regions() {
		if r.ID == "calendar" {
			if r.Rect.Y > MonthY {
				t.Errorf("calendar 从 y=%d 起，月份标题在 y=%d，跨月会漏刷", r.Rect.Y, MonthY)
				return
			}
			return
		}
	}
	t.Fatal("找不到 calendar 区域")
}

// 时钟数字格必须容得下设备字体的数字：STHeitiBold 在 200px 下
// advance 实测 111–114px，96px 的格会让字形越界约 15px。
// 总宽还要放得进左栏。
func TestClockDigitCellsFitLeftColumn(t *testing.T) {
	const measuredMaxAdvance = 114
	if DigitCellW < measuredMaxAdvance {
		t.Errorf("DigitCellW = %d，小于实测数字 advance %d，字形会越界",
			DigitCellW, measuredMaxAdvance)
	}
	if total := 4*DigitCellW + ColonCellW; total > LeftW {
		t.Errorf("时钟总宽 %d 超出左栏 %d", total, LeftW)
	}
	// 冒号格宽度决定冒号与右侧分钟数字的间距，不能随手改
	if ColonCellW != 48 {
		t.Errorf("ColonCellW = %d，期望 48", ColonCellW)
	}
}

// 版面骨架的数值同样来自 spec §4，逐字钉死。
// 中缝线 x=620 若被改成 600 会贴上左栏右缘，而 Regions() 的
// 覆盖测试仍会通过 —— 所以必须单独守。
func TestLayoutSkeletonMatchesSpec(t *testing.T) {
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"Margin", Margin, 56},
		{"LeftX", LeftX, 56},
		{"LeftW", LeftW, 544},
		{"DividerX", DividerX, 620},
		{"DividerY", DividerY, 56},
		{"DividerH", DividerH, 960},
		{"RightX", RightX, 682},
		{"RightW", RightW, 710},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d，期望 %d", c.name, c.got, c.want)
		}
	}
	// 骨架必须自洽：两栏加中缝正好铺满左右外边距之间
	if LeftX != Margin || CanvasW-RightX-RightW != Margin {
		t.Errorf("外边距不自洽: LeftX=%d CanvasW-RightX-RightW=%d Margin=%d",
			LeftX, CanvasW-RightX-RightW, Margin)
	}
	if DividerX <= LeftX+LeftW || DividerX >= RightX {
		t.Errorf("中缝 x=%d 不在左栏右缘 %d 与右栏左缘 %d 之间",
			DividerX, LeftX+LeftW, RightX)
	}
}
