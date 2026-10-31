package render

import "time"

// HitKind 是触摸落点的语义。
type HitKind int

const (
	HitNone HitKind = iota
	HitCalendarPrevMonth
	HitCalendarNextMonth
	HitCalendarDay
	HitCourseArea
	HitSettingsGear
	HitSettingsRow
)

// Hit 是一次触摸命中的结果。
type Hit struct {
	Kind HitKind
	Date time.Time // 仅 HitCalendarDay 有效
	Row  int       // 仅 HitSettingsRow 有效
}

// 触摸热区（横版画布坐标）。
const (
	// 月份标题行两端的翻月箭头：各 56×56
	MonthArrowLeftX  = RightX
	MonthArrowRightX = RightX + RightW - ArrowSize
	ArrowSize        = 56

	// 齿轮：课程区底部右侧。
	//
	// 不能贴着右缘：电量百分比右对齐到 1392（「98%」约占 1325..1391），
	// 齿轮热区放在 1308..1364 会与它重叠 —— 看得见电量，点到的却是设置。
	// 1240..1296 与电量之间留出 29px 空隙。
	GearX = 1240
	GearY = 1016
	GearW = 56
	GearH = 56

	// 设置页的六行条目。6 × 100 = 600，从 260 排到 860。
	SettingsFirstRowY = 260
	SettingsRowH      = 100
	SettingsRowCount  = 6
)

// 设置页各行的索引。必须与 render.settingsRows 的顺序一一对应：
// 顺序错了的后果是点「关闭」调了亮度或触发抓取，而界面上完全看不出异常。
const (
	SettingsRowBack            = 0
	SettingsRowRefresh         = 1
	SettingsRowBrightness      = 2
	SettingsRowRefreshSchedule = 3
	SettingsRowInfo            = 4
	SettingsRowClose           = 5
)

// 背光滑动条的几何（横版画布坐标）。
//
// 轨道在行内垂直居中，滑块圆心 = 轨道左端 + round(亮度/上限 × 轨道宽)。
// 命中不按轨道算：设置页的行整条 y 带都算命中（hitSettings 只看 y），
// 用户从滑块上按下去往外拖时，落手点常常已经在轨道之外。
const (
	SliderLabelX = 200
	SliderTrackX = 520
	SliderTrackW = 660
	SliderKnobR  = 14
	SliderValueX = 1200

	SliderTrackY = SettingsFirstRowY + SettingsRowBrightness*SettingsRowH + SettingsRowH/2
)

// HitTest 把画布坐标映射成操作目标。
//
// 设置页开着时只认设置页的行；关着时只认表盘元素。两者不重叠，
// 所以不需要「谁在上层」的判断。
func HitTest(x, y int, monthStart time.Time, settingsOpen bool) Hit {
	if settingsOpen {
		return hitSettings(x, y)
	}

	// 月份标题行
	if y >= MonthY && y < MonthY+ArrowSize {
		if x >= MonthArrowLeftX && x < MonthArrowLeftX+ArrowSize {
			return Hit{Kind: HitCalendarPrevMonth}
		}
		if x >= MonthArrowRightX && x < MonthArrowRightX+ArrowSize {
			return Hit{Kind: HitCalendarNextMonth}
		}
	}

	// 日期网格
	if x >= RightX && x < RightX+RightW && y >= CalGridY && y < CalGridY+CalRows*CalCellH {
		col := (x - RightX) / CalCellW
		row := (y - CalGridY) / CalCellH
		if col >= 0 && col < CalCols && row >= 0 && row < CalRows {
			return Hit{Kind: HitCalendarDay, Date: cellDate(monthStart, row, col)}
		}
	}

	// 课程区（电量与齿轮所在的底部横带排除在外）。
	// 下边界用 BatteryY 而不是 GearY：这条带是「电量所在横带」，
	// 语义上课程区止于电量带顶端。两者数值相同（都是 1016），
	// 但电量带才是它的定义。
	if x >= RightX && x < RightX+RightW &&
		y >= CoursesStartY && y < BatteryY {
		return Hit{Kind: HitCourseArea}
	}

	// 齿轮
	if x >= GearX && x < GearX+GearW && y >= GearY && y < GearY+GearH {
		return Hit{Kind: HitSettingsGear}
	}

	return Hit{Kind: HitNone}
}

// cellDate 返回日期网格里 (row,col) 对应的日期。
//
// 每周从周日开始：第一格是当月 1 日所在那一周的周日，所以前面几格
// 属于上个月。time.Weekday 的周日正好是 0，直接减即可。
//
// 注意：改周首日不能只改表头 —— 这里的补格推算必须一起改，否则整张
// 网格的日期会偏一天，而表头看着还是对的。
func cellDate(monthStart time.Time, row, col int) time.Time {
	first := time.Date(monthStart.Year(), monthStart.Month(), 1, 0, 0, 0, 0, monthStart.Location())
	gridStart := first.AddDate(0, 0, -int(first.Weekday()))
	return gridStart.AddDate(0, 0, row*CalCols+col)
}

// hitSettings 按 y 判断落在第几行。
//
// 这里有意不检查 x：设置页是整屏覆盖的，x 不属于任何具体的行，
// 所以「y 落在某一行的带里」就算点中那一行。不是漏了 x 判断。
func hitSettings(x, y int) Hit {
	for row := 0; row < SettingsRowCount; row++ {
		top := SettingsFirstRowY + row*SettingsRowH
		if y >= top && y < top+SettingsRowH {
			return Hit{Kind: HitSettingsRow, Row: row}
		}
	}
	return Hit{Kind: HitNone}
}
