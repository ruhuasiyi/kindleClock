package render

import "image"

// 画布是横版 1448×1072。设备的 framebuffer 是竖版 1072×1448，
// 由 display 包负责旋转。
const (
	CanvasW = 1448
	CanvasH = 1072
)

// 版面骨架（数值取自 spec §4）。
const (
	Margin = 56 // 外边距

	LeftX = 56  // 左栏左边界
	LeftW = 544 // 左栏宽度

	// 中缝竖线是静态元素：只在整屏刷新时绘制，不落在任何刷新区域内
	// （x=620 位于左栏右缘 600 与右栏左缘 682 之间），
	// 因此不需要为它单列区域，也不违反「区域互不重叠」。
	DividerX = 620
	DividerY = 56
	DividerH = 960

	// 右栏左缘 682 而非 680：设备字体 STHeiti 的「2」有负左边距，
	// 月份标题「2026 年 十月」按 x=680 起画时，首字墨迹落在 x=679 ——
	// 那是 621..681 的中缝盲带，不属于任何刷新区域，永远刷不新。
	// 让出 2px，几何上与 spec §4.3 的「起始 x = 682」一致。
	// 宽度相应 712 → 710，右边界保持 1392 不变。
	RightX = 682
	RightW = 710
)

// Rect 是横版坐标下的矩形。右下为开区间，与 image.Rectangle 一致。
type Rect struct {
	X, Y, W, H int
}

func (r Rect) Right() int  { return r.X + r.W }
func (r Rect) Bottom() int { return r.Y + r.H }

func (r Rect) Standard() image.Rectangle {
	return image.Rect(r.X, r.Y, r.Right(), r.Bottom())
}

// Region 是一个可独立刷新的屏幕区域。
//
// Waveform 是 fbink 的波形名。时钟区用 REAGL（实测 13 ms，最快），
// 其余用 GC16。A2 在 PW3 上反而慢 8 倍，禁用。
type Region struct {
	ID       string
	Rect     Rect
	Waveform string
}

// Regions 返回六个刷新区域，顺序固定（用于稳定的日志与测试）。
//
// 这些矩形必须互不重叠：重叠会导致同一像素被两个区域写入，
// fbink 的局部刷新会互相覆盖，产生闪烁。
func Regions() []Region {
	return []Region{
		{ID: "clock", Rect: Rect{X: 56, Y: 250, W: 544, H: 220}, Waveform: "REAGL"},
		// 高度 178 而不是 128：y 200..250 是「星期」与「时钟」之间那条
		// 农历/节气行。它原本不在任何刷新区域内 —— 往里画的东西首次显示后
		// 永远刷不新。区域必须把这一行吃进来。
		{ID: "date", Rect: Rect{X: 56, Y: 72, W: 544, H: 178}, Waveform: "GC16"},
		{ID: "weather", Rect: Rect{X: 56, Y: 540, W: 544, H: 328}, Waveform: "GC16"},
		// 从 y=72 起：月份标题画在 MonthY=72，若从 164（星期表头）起，
		// 跨月时标题永远刷不新。
		{ID: "calendar", Rect: Rect{X: 682, Y: 72, W: 710, H: 548}, Waveform: "GC16"},
		// 到画布底：翻页页码画在 y≈1024..1064，
		// 若只到 1016，页码不会被刷新。
		{ID: "courses", Rect: Rect{X: 682, Y: 632, W: 710, H: 440}, Waveform: "GC16"},
		// 高度 54 而非 40：底部留白与右栏对齐（右栏到 1072），
		// 也保证 TestRegionsCoverBothColumns 的「左栏覆盖到 CanvasH-Margin」成立。
		{ID: "footer", Rect: Rect{X: 56, Y: 962, W: 544, H: 54}, Waveform: "GL16"},
	}
}

// 左栏各元素的位置（spec §4.2）
const (
	DateY     = 72
	WeekdayY  = 156
	LunarY    = 205 // 农历/节气行的区域顶端
	ClockY    = 250
	WeatherY  = 540
	WeatherH  = 96
	TempY     = 660
	HighLowY  = 768
	AQIY      = 828
	DataTimeY = 968
)

// 右栏—月历（spec §4.3）
const (
	MonthY       = 72
	CalDivider1Y = 148
	WeekHeaderY  = 164
	CalGridY     = 208
	CalCellW     = 101
	CalCellH     = 64
	CalCols      = 7
	CalRows      = 6
	CalDivider2Y = 616
)

// 右栏—课程（spec §4.4）
const (
	CoursesTitleY = 632
	CoursesStartY = 680
	CourseRowH    = 56
	CourseMaxRows = 6
	CourseTimeW   = 88
	CourseRoomW   = 240

	BatteryY = 1016 // 电量所在横带：课程行最多 6 行到 y=1016，往下是空白
)

// 时钟数字格（spec §4.5）
//
// DigitCellW 由 96 放宽到 118：用设备字体 STHeitiBold 实测，200px 下
// 数字 advance 是 111–114px、冒号 55px，96px 的格会让字形越界约 15px；
// 且「1」比「8」窄，分钟跳变时视觉上会左右挪动。固定格宽 = 数字不动。
// 总宽 4×118+48 = 520，居中于 544 的左栏，左右各留 12px。
const (
	DigitCellW = 118
	ColonCellW = 48
	ClockCellH = 220
)
