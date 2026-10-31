package render

import (
	"testing"
	"time"
)

func TestHitTestCalendarArrows(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 月份标题行两端的热区
	if h := HitTest(MonthArrowLeftX+ArrowSize/2, MonthY+10, month, false); h.Kind != HitCalendarPrevMonth {
		t.Fatalf("左侧箭头应命中上一月，得到 %v", h.Kind)
	}
	if h := HitTest(MonthArrowRightX+ArrowSize/2, MonthY+10, month, false); h.Kind != HitCalendarNextMonth {
		t.Fatalf("右侧箭头应命中下一月，得到 %v", h.Kind)
	}
}

func TestHitTestCalendarDay(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 周首日是周日：10 月 1 日周四落在第 1 行第 5 列（列 4，0 起）
	x := RightX + 4*CalCellW + CalCellW/2
	y := CalGridY + 0*CalCellH + CalCellH/2
	h := HitTest(x, y, month, false)
	if h.Kind != HitCalendarDay {
		t.Fatalf("日期格应命中 HitCalendarDay，得到 %v", h.Kind)
	}
	if h.Date.Day() != 1 || h.Date.Month() != time.October {
		t.Fatalf("应命中 10 月 1 日，得到 %s", h.Date.Format("01-02"))
	}
}

// 点到上/下月的灰色补格时，日期应当属于相邻月份 —— 由调用方据此切换月份。
func TestHitTestAdjacentMonthCell(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 周首日是周日：10 月 1 日周四，前面四格是 9 月 27/28/29/30，
	// 左上角第一格是 9 月 27 日（周日）。
	x := RightX + 0*CalCellW + CalCellW/2
	y := CalGridY + CalCellH/2
	h := HitTest(x, y, month, false)
	if h.Kind != HitCalendarDay || h.Date.Month() != time.September || h.Date.Day() != 27 {
		t.Fatalf("应命中 9 月 27 日，得到 %v %s", h.Kind, h.Date.Format("01-02"))
	}
}

func TestHitTestCourseAreaAndGear(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if h := HitTest(RightX+100, CoursesStartY+10, month, false); h.Kind != HitCourseArea {
		t.Fatalf("课程区应命中 HitCourseArea，得到 %v", h.Kind)
	}
	// 齿轮热区 1240,1016,56,56 —— 中心 (1268,1044)
	if h := HitTest(1268, 1044, month, false); h.Kind != HitSettingsGear {
		t.Fatalf("齿轮应命中 HitSettingsGear，得到 %v", h.Kind)
	}
	// 电量右对齐在 1392 附近；原齿轮位置 1330 必须已经让出来，
	// 否则那里是电量的字，点下去却进了设置页。
	if h := HitTest(1330, 1044, month, false); h.Kind == HitSettingsGear {
		t.Fatalf("原齿轮位置 1330,1044 仍命中齿轮 —— 与电量重叠")
	}
}

func TestHitTestLeftColumnIsNone(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 左栏（时钟、天气）目前没有任何交互
	if h := HitTest(300, 400, month, false); h.Kind != HitNone {
		t.Fatalf("左栏不该有交互，得到 %v", h.Kind)
	}
}

func TestHitTestSettingsRows(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for row := 0; row < SettingsRowCount; row++ {
		y := SettingsFirstRowY + row*SettingsRowH + SettingsRowH/2
		h := HitTest(CanvasW/2, y, month, true)
		if h.Kind != HitSettingsRow || h.Row != row {
			t.Fatalf("第 %d 行应命中 HitSettingsRow{%d}，得到 %v{%d}", row, row, h.Kind, h.Row)
		}
	}
	// 设置页开着时，点原表盘上的东西不应命中。
	//
	// 取样点必须落在设置页行带（y 260..760）之外 —— 早先写的是
	// (RightX+100, CoursesStartY+10) = (782,690)，而 690 正好在第 3 行带里，
	// hitSettings 只看 y，那个点必然命中，与「不该命中」的断言自相矛盾。
	if h := HitTest(GearX+10, GearY+10, month, true); h.Kind != HitNone {
		t.Fatalf("设置页开着时不该命中齿轮，得到 %v", h.Kind)
	}
}

// 背光行整条 y 带都命中，不要求落在轨道那条线上：
// 用户会从滑块上按下去再往外拖，落手点常常在轨道之外。
func TestHitTestBrightnessRowCoversWholeBand(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rowTop := SettingsFirstRowY + SettingsRowBrightness*SettingsRowH

	for _, y := range []int{
		rowTop,                    // 行首
		SliderTrackY - 40,         // 轨道上方 40px
		SliderTrackY,              // 轨道上
		SliderTrackY + 40,         // 轨道下方 40px
		rowTop + SettingsRowH - 1, // 行尾
	} {
		// x 取远左、轨道本身、远右：命中只看 y，x 不设限。
		for _, x := range []int{100, SliderTrackX, CanvasW - 100} {
			h := HitTest(x, y, month, true)
			if h.Kind != HitSettingsRow || h.Row != SettingsRowBrightness {
				t.Fatalf("(x=%d, y=%d) 应命中背光行（Row %d），得到 %v Row=%d",
					x, y, SettingsRowBrightness, h.Kind, h.Row)
			}
		}
	}
}
