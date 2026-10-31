package render

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"

	"github.com/rhsy/kindleclock/src/internal/state"
	"github.com/rhsy/kindleclock/src/internal/ui"
)

// 字号（像素）。集中在这里，启动时一次性预热所有字面。
const (
	pxDay        = 64
	pxWeekday    = 36
	pxLunar      = 32
	pxClock      = 200
	pxWeatherTx  = 64
	pxTemp       = 80
	pxHighLow    = 34
	pxAQI        = 28
	pxDataTime   = 26
	pxMonth      = 48
	pxWeekHead   = 28
	pxDayNum     = 32 // 日历格日号：要为格子下半部的农历腾出空间
	pxToday      = 40 // 今天的日号：反白格内用 40px Bold（spec §4.3）
	pxCalLunar   = 16 // 日历格里的农历/节气：格高只有 64px
	pxCourseTtl  = 26
	pxCourseRow  = 30
	pxCourseSm   = 26
	pxBattery    = 28
	pxAdjustMark = 14 // 调休「班」字：画在日号右上角，给农历让位

	pxSettingsRow  = 40 // 设置页四行条目
	pxSettingsInfo = 24 // 「设备信息」行下方的小字
)

// Renderer 把 Snapshot 画成横版位图。
//
// 纯函数式：同样的 Snapshot 一定产出逐像素相同的图 —— 脏区域比对
// 完全依赖这个性质。
type Renderer struct {
	// fonts 只用于保持与字面来源的关联；字面的生命周期归 Fonts 所有，
	// Renderer.Close 不会关闭它。保留这个字段是为了让「这个 Renderer
	// 用的是哪份字体」在调试时可见，也方便将来 Close 真正需要时使用。
	fonts *Fonts

	faceDay           font.Face
	faceWeekday       font.Face
	faceLunar         font.Face
	faceClock         font.Face
	faceWeather       font.Face
	faceTemp          font.Face
	faceHighLow       font.Face
	faceAQI           font.Face
	faceDataTime      font.Face
	faceMonth         font.Face
	faceWeekHead      font.Face
	faceDayNum        font.Face
	faceToday         font.Face
	faceCalLunar      font.Face
	faceCourseTtl     font.Face
	faceCourseRow     font.Face
	faceCourseRowBold font.Face
	faceCourseSm      font.Face
	faceBattery       font.Face
	faceAdjustMark    font.Face
	faceSettingsRow   font.Face
	faceSettingsInfo  font.Face
}

// NewRenderer 预热全部字面。字面生成在构造期完成，主循环里只做绘制。
func NewRenderer(f *Fonts) (*Renderer, error) {
	r := &Renderer{fonts: f}
	var err error
	get := func(s Style, px float64) font.Face {
		if err != nil {
			return nil
		}
		var face font.Face
		face, err = f.Face(s, px)
		return face
	}
	r.faceDay = get(Bold, pxDay)
	r.faceWeekday = get(Regular, pxWeekday)
	r.faceLunar = get(Regular, pxLunar)
	r.faceClock = get(Bold, pxClock)
	r.faceWeather = get(Bold, pxWeatherTx)
	r.faceTemp = get(Bold, pxTemp)
	r.faceHighLow = get(Regular, pxHighLow)
	r.faceAQI = get(Regular, pxAQI)
	r.faceDataTime = get(Regular, pxDataTime)
	r.faceMonth = get(Bold, pxMonth)
	r.faceWeekHead = get(Regular, pxWeekHead)
	r.faceDayNum = get(Regular, pxDayNum)
	r.faceToday = get(Bold, pxToday)
	r.faceCalLunar = get(Regular, pxCalLunar)
	r.faceCourseTtl = get(Regular, pxCourseTtl)
	r.faceCourseRow = get(Regular, pxCourseRow)
	r.faceCourseRowBold = get(Bold, pxCourseRow)
	r.faceCourseSm = get(Regular, pxCourseSm)
	r.faceBattery = get(Regular, pxBattery)
	r.faceAdjustMark = get(Regular, pxAdjustMark)
	r.faceSettingsRow = get(Regular, pxSettingsRow)
	r.faceSettingsInfo = get(Regular, pxSettingsInfo)
	if err != nil {
		return nil, fmt.Errorf("预热字面: %w", err)
	}
	return r, nil
}

// Render 画一张完整的横版位图。
//
// u 是交互状态（翻到哪个月、选中哪天）。它和 Snapshot 一起构成全部输入：
// 同样的 Snapshot + 同样的 State 必须产出逐像素相同的图。
func (r *Renderer) Render(snap state.Snapshot, u ui.State) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, CanvasW, CanvasH))
	for i := range img.Pix {
		img.Pix[i] = 255
	}

	// 设置页整屏覆盖：不画表盘，否则设置页关掉时脏区比对会漏掉
	// 「设置页盖住过的那些区域」。
	//
	// 刷新区域约束的例外：设置页的墨迹横跨整个画布，必然落在六个刷新
	// 区域之外。这是有意的 —— 它配合主循环的全屏刷新显示，不是漏了
	// 区域约束。关掉设置页时整屏重画，所以被盖过的残影不会留下。
	if u.SettingsOpen {
		r.drawSettings(img, snap)
		return img
	}

	r.drawDate(img, snap)
	r.drawDayInfo(img, snap)
	r.drawClock(img, snap)
	r.drawWeather(img, snap)
	r.drawCalendar(img, snap, u)
	r.drawCourses(img, snap, u)
	r.drawBattery(img, snap)
	r.drawGear(img)
	r.drawFooter(img, snap)

	// 中缝是静态元素：spec §4.1 要求 1px 黑线，位于 x=620（左栏右缘 600 与
	// 右栏左缘 682 之间），不属于任何刷新区域，只在整屏刷新时出现。
	VLine(img, DividerX, DividerY, DividerH, ColorBlack)
	return img
}

// 设置页的六行。索引与 hit.go 的 SettingsRow* 常量一一对应，
// 改顺序要同步改 hit.go 的几何。
var settingsRows = []string{
	"返回 Kindle 界面",
	"立即全屏刷新",
	"背光亮度",
	"刷新课表",
	"设备信息",
	"关闭",
}

// drawSettings 画整屏设置页。
//
// 这里画的像素故意不落在六个局部刷新区域内（见 Render 里的说明）：
// 设置页始终配合主循环的全屏刷新。
func (r *Renderer) drawSettings(img *image.Gray, snap state.Snapshot) {
	for i, label := range settingsRows {
		top := SettingsFirstRowY + i*SettingsRowH
		// 「设备信息」行下方还要放一行小字：标签上移，把行尾让出来。
		// 其余行用整行高度垂直居中。
		labelH := SettingsRowH
		if i == SettingsRowInfo {
			labelH = 60
		}
		baseline := baselineIn(r.faceSettingsRow, top, labelH)

		switch i {
		case SettingsRowBrightness:
			// 背光行不居中标签：标签固定左对齐，右边是滑动条与数值。
			r.drawBrightnessSlider(img, snap.Info, baseline)
		case SettingsRowInfo:
			TextCentered(img, r.faceSettingsRow, label, 0, baseline, CanvasW, ColorBlack)
			info := settingsInfoText(snap.Info)
			TextCentered(img, r.faceSettingsInfo,
				Ellipsize(r.faceSettingsInfo, info, CanvasW-2*Margin),
				0, baselineIn(r.faceSettingsInfo, top+56, 38), CanvasW, ColorLight)
		default:
			TextCentered(img, r.faceSettingsRow, label, 0, baseline, CanvasW, ColorBlack)
			if i == SettingsRowRefreshSchedule {
				// 状态靠右：刷新中… / 失败 / 上次 HH:MM。
				if status := settingsScheduleStatus(snap.Info); status != "" {
					TextRight(img, r.faceSettingsRow, status,
						CanvasW-Margin, baseline, ColorGray)
				}
			}
		}

		if i < len(settingsRows)-1 {
			HLine(img, Margin, top+SettingsRowH-1, CanvasW-2*Margin, ColorLight)
		}
	}
}

// drawBrightnessSlider 画「背光亮度」行：标签 + 轨道 + 已填充段 + 滑块 + 数值。
//
// 滑块位置由 Snapshot.Info 算出，不读任何设备状态 —— 渲染仍是纯函数。
// 读不到亮度（Brightness < 0 或上限未知）时只画轨道和「—」：没有可信的
// 数值就不放滑块，免得把一个猜测画成事实。
func (r *Renderer) drawBrightnessSlider(img *image.Gray, info state.SystemInfo, baseline int) {
	TextColumnLeft(img, r.faceSettingsRow, "背光亮度", SliderLabelX, baseline, ColorBlack)

	// 轨道底座：2px 浅灰线，行内垂直居中。
	HLine(img, SliderTrackX, SliderTrackY-1, SliderTrackW, ColorLight)
	HLine(img, SliderTrackX, SliderTrackY, SliderTrackW, ColorLight)

	if info.Brightness < 0 || info.BrightnessMax <= 0 {
		TextColumnLeft(img, r.faceSettingsRow, "—", SliderValueX, baseline, ColorBlack)
		return
	}

	level := info.Brightness
	if level > info.BrightnessMax {
		level = info.BrightnessMax
	}
	knobX := SliderTrackX + int(math.Round(
		float64(level)/float64(info.BrightnessMax)*float64(SliderTrackW)))

	// 已填充部分用黑线，深于底座；宽度为 0（0 档）时不画。
	if w := knobX - SliderTrackX; w > 0 {
		FillRect(img, SliderTrackX, SliderTrackY-1, w, 2, ColorBlack)
	}
	Circle(img, knobX, SliderTrackY, SliderKnobR, ColorBlack)
	TextColumnLeft(img, r.faceSettingsRow, strconv.Itoa(info.Brightness),
		SliderValueX, baseline, ColorBlack)
}

// settingsScheduleStatus 是「刷新课表」行右侧的状态文字。
//
// 优先级：进行中 > 失败 > 上次成功时刻。失败优先于「上次」是有意的 ——
// 刚失败就把旧时间显示回去，会让人以为刷新成功了。
//
// 只对快照里的时间做格式化，不读系统时钟：渲染仍是纯函数。
func settingsScheduleStatus(info state.SystemInfo) string {
	switch {
	case info.Refreshing:
		return "刷新中…"
	case info.ScheduleRefreshFailed:
		return "失败"
	case !info.ScheduleAt.IsZero():
		return "上次 " + info.ScheduleAt.Format("15:04")
	default:
		return ""
	}
}

// fmtHM 把抓取时刻格式化成 HH:MM；零值表示没有数据，显示「无」。
func fmtHM(t time.Time) string {
	if t.IsZero() {
		return "无"
	}
	return t.Format("15:04")
}

// settingsInfoText 拼「设备信息」行的小字。
//
// 空字段整段跳过，不留下「｜ 固件  ｜」这样的空段；周次未知（<=0）时
// 明确说「教学周未知」，而不是显示「第 0 教学周」。
func settingsInfoText(info state.SystemInfo) string {
	parts := make([]string, 0, 5)
	if info.ProgramVersion != "" {
		parts = append(parts, info.ProgramVersion)
	}
	if info.Firmware != "" {
		parts = append(parts, "固件 "+info.Firmware)
	}
	if info.Week > 0 {
		parts = append(parts, fmt.Sprintf("第 %d 教学周", info.Week))
	} else {
		parts = append(parts, "教学周未知")
	}
	parts = append(parts,
		"天气 "+fmtHM(info.WeatherAt),
		"课表 "+fmtHM(info.TimetableAt))
	return strings.Join(parts, " ｜ ")
}

// Close 释放与 Renderer 相关的资源。
//
// 字面由 Fonts 持有并缓存，Renderer 只是引用方；这里刻意不关闭字体，
// 因为同一份 Fonts 可能被别的 Renderer 或预览路径复用 —— 关闭它的
// 责任在构造 Fonts 的一方。保留这个方法是为了让调用方可以用 defer
// 统一写清理逻辑，也为将来可能出现的自有资源留出口。
func (r *Renderer) Close() error {
	return nil
}

// baselineIn 返回让文字在 [top, top+height] 内垂直居中的基线 y。
//
// 用 font.Metrics 而不是拍脑袋的系数：中文字体的 ascent 差异很大，
// 用固定的 0.8 会在 STHeiti 与 STSong 之间来回偏移。
func baselineIn(face font.Face, top, height int) int {
	m := face.Metrics()
	textH := int(m.Ascent+m.Descent) >> 6
	if textH <= 0 {
		return top + height/2
	}
	return top + (height-textH)/2 + int(m.Ascent>>6)
}

// ---- 左栏 ----

func (r *Renderer) drawDate(img *image.Gray, snap state.Snapshot) {
	t := snap.Now
	dayText := fmt.Sprintf("%d月%d日", int(t.Month()), t.Day())
	weekday := weekdayCN(t.Weekday())

	TextColumnLeft(img, r.faceDay, dayText, LeftX, baselineIn(r.faceDay, DateY, 72), ColorBlack)
	TextColumnLeft(img, r.faceWeekday, weekday, LeftX, baselineIn(r.faceWeekday, WeekdayY, 44), ColorGray)

	// 法定节假日标签右对齐在日期行上。用 36px（与星期同行字号）而不是 64px：
	// 它是一句附注，不该和日期争夺注意力。
	//
	// 标签可能变长（接口字段没有长度保证），按日期区宽度省略：右对齐时
	// 超宽会一直画到 x=0，落进左边距那条永不刷新的盲带。
	if tag := snap.Day.Tag(); tag != "" {
		TextRight(img, r.faceWeekday, Ellipsize(r.faceWeekday, tag, LeftW),
			LeftX+LeftW, baselineIn(r.faceWeekday, DateY+20, 44), ColorLight)
	}
}

// drawDayInfo 画「星期」与「时钟」之间那一行的农历或节气。
//
// 这一行占 y 200..250 ——— 原本是一条没有任何刷新区域覆盖的空白带，
// 所以 date 区域从 y72..200 扩到了 y72..250。否则这行首次显示后永远刷不新。
//
// 节气优先于农历日：一个月只有两次节气，更值得一眼看到。
func (r *Renderer) drawDayInfo(img *image.Gray, snap state.Snapshot) {
	line := snap.Day.Line()
	if line == "" {
		return
	}
	TextColumnLeft(img, r.faceLunar, line, LeftX,
		baselineIn(r.faceLunar, LunarY, 44), ColorLight)
}

func (r *Renderer) drawClock(img *image.Gray, snap state.Snapshot) {
	hh := fmt.Sprintf("%02d", snap.Now.Hour())
	mm := fmt.Sprintf("%02d", snap.Now.Minute())

	// 固定格子：4 个 118px 数字格 + 1 个 48px 冒号格 = 520px，居中于 544 宽左栏
	total := 4*DigitCellW + ColonCellW
	startX := LeftX + (LeftW-total)/2
	baseline := baselineIn(r.faceClock, ClockY, ClockCellH)

	// 用墨迹居中而非 advance 居中：数字「1」的 advance 与墨迹中心差 11px，
	// 按 advance 居中会让分钟跳变时它左右挪动。
	cell := func(ch string, cellX int) {
		TextInkCentered(img, r.faceClock, ch, cellX, baseline, DigitCellW, ColorBlack)
	}
	cell(hh[0:1], startX)
	cell(hh[1:2], startX+DigitCellW)
	drawColon(img, startX+2*DigitCellW, ClockY+ClockCellH/2)
	cell(mm[0:1], startX+2*DigitCellW+ColonCellW)
	cell(mm[1:2], startX+3*DigitCellW+ColonCellW)
}

// drawColon 画两个方块。不走字体：冒号的字形高度在不同字体间差异太大，
// 自己画能保证每分钟的像素完全一致。
func drawColon(img *image.Gray, cellX, centerY int) {
	const size = 14
	const halfGap = 20
	cx := cellX + ColonCellW/2
	FillRect(img, cx-size/2, centerY-halfGap-size/2, size, size, ColorBlack)
	FillRect(img, cx-size/2, centerY+halfGap-size/2, size, size, ColorBlack)
}

func (r *Renderer) drawWeather(img *image.Gray, snap state.Snapshot) {
	if snap.Weather == nil {
		TextColumnLeft(img, r.faceHighLow, "—", LeftX, baselineIn(r.faceHighLow, WeatherY, WeatherH), ColorLight)
		return
	}
	w := snap.Weather

	drawWeatherIcon(img, LeftX, WeatherY, WeatherH, w.Text)

	// 天气文本必须截断：weather 区止于 x=600，而「晴转多云有雷阵雨」这类
	// 8 字文本按 64px 画会到 x=677 —— 落进 600..682 的中缝盲带，
	// 那里的像素不属于任何刷新区域，永远不会被刷新（真机上是截断 + 残影）。
	// 可用宽度 = 左栏右缘 600 − 文本起点 168 = 432，约 6 个汉字。
	const weatherTextX = LeftX + 112
	textMaxW := LeftX + LeftW - weatherTextX
	Text(img, r.faceWeather,
		Ellipsize(r.faceWeather, w.Text, textMaxW),
		weatherTextX, baselineIn(r.faceWeather, WeatherY, WeatherH), ColorBlack)
	TextColumnLeft(img, r.faceTemp, fmt.Sprintf("%d°", w.Temp), LeftX, baselineIn(r.faceTemp, TempY, 96), ColorBlack)
	TextColumnLeft(img, r.faceHighLow,
		fmt.Sprintf("%d° / %d°", w.TempMin, w.TempMax),
		LeftX, baselineIn(r.faceHighLow, HighLowY, 48), ColorGray)

	if w.AQI != nil {
		TextColumnLeft(img, r.faceAQI,
			fmt.Sprintf("%s %d", w.AQICategory, *w.AQI),
			LeftX, baselineIn(r.faceAQI, AQIY, 36), ColorGray)
	}

	// 数据时间与滞后标记不在这里画：DataTimeY=968 落在 footer 区
	// （962..1002），由 drawFooter 统一负责。谁的区域谁画，刷新才能对上。
}

// ---- 右栏 ----

// weekdayHeaders 是日历表头，从周日开始，与 hit.go 的 cellDate 一一对应。
//
// 单独提成包级变量是为了让测试能直接钉住顺序：gofont 不含中文，
// 「日一二三四五六」都画成同一个缺字方框，逐像素比较看不出顺序变化。
var weekdayHeaders = [...]string{"日", "一", "二", "三", "四", "五", "六"}

func (r *Renderer) drawCalendar(img *image.Gray, snap state.Snapshot, u ui.State) {
	monthStart := ui.MonthStart(snap.Now, u.MonthOffset)
	baseline := baselineIn(r.faceMonth, MonthY, 56)

	// 标题右移一个箭头宽度：左箭头热区是 [RightX, RightX+ArrowSize)，
	// 标题从 RightX 起画的话，点年份「20…」会触发上一月。
	// 仍然用 TextColumnLeft：STHeiti 的「2」有负左边距，贴着右栏左缘画会
	// 落进 621..681 的中缝盲带。
	TextColumnLeft(img, r.faceMonth,
		fmt.Sprintf("%d 年 %s", monthStart.Year(), monthCN(monthStart.Month())),
		RightX+ArrowSize, baseline, ColorBlack)

	// 翻月箭头。热区与标题同高，各自 56px 宽。
	// 箭头用字体字形 ‹ ›。设备 STHeiti 实测有（GlyphIndex 574/575）。
	// 若换了缺该字形的字体，这里会显示成方框 —— 是可见的失败，不是静默错误。
	TextCentered(img, r.faceMonth, "‹", MonthArrowLeftX, baseline, ArrowSize, ColorGray)
	TextCentered(img, r.faceMonth, "›", MonthArrowRightX, baseline, ArrowSize, ColorGray)

	HLine(img, RightX, CalDivider1Y, RightW, ColorBlack)

	// 一周从周日开始。表头顺序与 hit.go 的 cellDate 必须一致：
	// 只改表头不改补格推算，日期会整体偏一天，而表头看着还是对的。
	for i, h := range weekdayHeaders {
		c := ColorGray
		if isWeekendCol(i) {
			c = ColorLight
		}
		TextCentered(img, r.faceWeekHead, h,
			RightX+i*CalCellW, baselineIn(r.faceWeekHead, WeekHeaderY, 32), CalCellW, c)
	}

	// 网格：日期一律由 hit.go 的 cellDate 推算，与命中测试共用同一份补格逻辑。
	// 两边各写一遍的话，点的地方和画的地方迟早对不上，而且现有测试抓不到。
	for cellIdx := 0; cellIdx < CalCols*CalRows; cellIdx++ {
		row, col := cellIdx/CalCols, cellIdx%CalCols
		date := cellDate(monthStart, row, col)
		cellX := RightX + col*CalCellW
		cellY := CalGridY + row*CalCellH

		inMonth := date.Year() == monthStart.Year() && date.Month() == monthStart.Month()

		// 补格（非本月）只画浅灰日号，不参与今天/选中/节假日/农历任何一种装饰。
		//
		// 反例：今天 10-31 翻到 11 月，11 月网格首行是 10 月的补格 26..31 ——
		// 给其中的 31 画今天黑块，会让人以为「11 月里有个 31 号」。
		// 日历视图是「这个月」，补格只是让网格看起来完整。
		if !inMonth {
			TextCentered(img, r.faceDayNum, fmt.Sprintf("%d", date.Day()),
				cellX, calDayBaseline(cellY), CalCellW, ColorLight)
			continue
		}

		isToday := sameDate(date, snap.Now)
		// 跟随今天时 SelectedDate 是零值，不画选中态；否则非零的零值时间
		// 会与所有格子都不相等，虽不致命但多一次无谓的比较。
		isSelected := !u.FollowToday() && sameDate(date, u.SelectedDate)
		info := snap.Calendar[date.Format("2006-01-02")]
		lunar := calendarCellLunar(info)

		switch {
		case isToday:
			// 今天：整格填黑 + 数字反白 40px Bold（spec §4.3）
			//
			// 黑块必须画满整格高度：农历基线在 cellY+58，16px 字的下缘会到
			// cellY+62 左右；原来块只到 cellY+60，探出去的几个像素是白字画在
			// 白底上，等于被切掉一截。
			FillRoundRect(img, cellX, cellY, CalCellW, CalCellH, 8, ColorBlack)
			TextCentered(img, r.faceToday, fmt.Sprintf("%d", date.Day()),
				cellX, calDayBaseline(cellY), CalCellW, ColorWhite)
			// 黑块上的农历也反白，否则浅灰压在黑底上几乎看不见。
			if lunar != "" {
				TextCentered(img, r.faceCalLunar, lunar,
					cellX, cellY+58, CalCellW, ColorWhite)
			}
		case isSelected:
			// 选中但不是今天：描边，数字不变色 —— 与默认分支同规则
			// （周末浅灰、工作日黑）。
			// strokeRoundRect 只写环带，不会擦掉下面画上的日号 —— 但为了
			// 将来换成别的实现时也不出错，仍然先描边后画数字。
			strokeRoundRect(img, cellX, cellY+4, CalCellW, CalCellH-8, 8, 2, ColorGray)
			TextCentered(img, r.faceDayNum, fmt.Sprintf("%d", date.Day()),
				cellX, calDayBaseline(cellY), CalCellW, cellDayColor(col))
			if lunar != "" {
				TextCentered(img, r.faceCalLunar, lunar,
					cellX, cellY+58, CalCellW, ColorLight)
			}
		default:
			// 周末用灰（spec §4.3）。
			TextCentered(img, r.faceDayNum, fmt.Sprintf("%d", date.Day()),
				cellX, calDayBaseline(cellY), CalCellW, cellDayColor(col))
			if lunar != "" {
				TextCentered(img, r.faceCalLunar, lunar,
					cellX, cellY+58, CalCellW, ColorLight)
			}
		}

		// 节假日标记放在右上角：格底已经被农历占满，圆点会压在上面。
		// 「休/班」比一个圆点多说了「是放假还是调休上班」，且是中文日历的固有约定。
		switch info.HolidayKind {
		case state.HolidayRest:
			TextRight(img, r.faceAdjustMark, "休",
				cellX+CalCellW-6, baselineIn(r.faceAdjustMark, cellY+2, 24), ColorLight)
		case state.HolidayWorkdayAdjust:
			TextRight(img, r.faceAdjustMark, "班",
				cellX+CalCellW-6, baselineIn(r.faceAdjustMark, cellY+2, 24), ColorLight)
		}
	}
	HLine(img, RightX, CalDivider2Y, RightW, ColorBlack)
}

// calDayBaseline 是日历格里日号的基线。固定到格顶 +38 而不是垂直居中：
// 格子下半部要留给 16px 的农历/节气行（基线 cellY+58）。
func calDayBaseline(cellY int) int { return cellY + 38 }

// isWeekendCol 报告网格列是否为周末。周首日是周日，所以周六在最后一列。
func isWeekendCol(col int) bool { return col == 0 || col == CalCols-1 }

// cellDayColor 是日号颜色：周末浅灰、工作日黑（spec §4.3）。
func cellDayColor(col int) color.Color {
	if isWeekendCol(col) {
		return ColorGray
	}
	return ColorBlack
}

// calendarCellLunar 返回格子底部该显示什么：有节气就显示节气，
// 否则只显示农历日名（不带月份）。
//
// 用户要求的是「农历不显示月份」，不是「不要节气」—— 节气一个月只有
// 两次，比「初八」这种日常信息更值得一眼看到，所以仍然优先。
func calendarCellLunar(d state.DayInfo) string {
	if d.SolarTerm != "" {
		return d.SolarTerm
	}
	return d.LunarDayOnly()
}

func (r *Renderer) drawCourses(img *image.Gray, snap state.Snapshot, u ui.State) {
	tt := snap.Timetable

	// 标题跟随选中日期：今天显示「今日课程」，别的日子显示具体日期。
	// 选中日带上完整农历、节假日名/节气 —— 日历格只能放日名，不点进来
	// 看不出是哪个月的农历。
	//
	// 数据必须查 snap.Calendar[选中日]：snap.Day 是「今天」的，选中别的日子
	// 时用它会显示成今天的农历/节日。选中日在别的月份时 Calendar 里没有，
	// 就只留日期。
	title := courseTitle(snap, u)
	TextColumnLeft(img, r.faceCourseTtl, Ellipsize(r.faceCourseTtl, title, RightW), RightX,
		baselineIn(r.faceCourseTtl, CoursesTitleY, 32), ColorGray)

	if !tt.Available {
		msg := tt.Message
		if msg == "" {
			msg = "课表暂不可用"
		}
		TextColumnLeft(img, r.faceCourseRow, Ellipsize(r.faceCourseRow, msg, RightW),
			RightX, baselineIn(r.faceCourseRow, CoursesStartY+40, 56), ColorLight)
		if !tt.FetchedAt.IsZero() {
			TextColumnLeft(img, r.faceCourseSm, "上次成功 "+tt.FetchedAt.Format("15:04"),
				RightX, baselineIn(r.faceCourseSm, CoursesStartY+100, 32), ColorLight)
		}
		return
	}

	if len(tt.Courses) == 0 {
		msg := tt.Message
		if msg == "" {
			msg = "今日暂无后续课程"
		}
		// 同样必须截断：TextCentered 用 RightW 居中，长消息会越过右栏左右边界
		// 落进中缝盲带或画布外。
		TextCentered(img, r.faceCourseRow,
			Ellipsize(r.faceCourseRow, msg, RightW-16), RightX,
			baselineIn(r.faceCourseRow, CoursesStartY+120, 56), RightW, ColorLight)
		return
	}

	// 分页：越界要夹紧，否则用户滑过头会看到一片空白。
	// len(tt.Courses) > 0 已由上面的早退分支保证，所以 pages 至少为 1。
	pages := (len(tt.Courses) + CourseMaxRows - 1) / CourseMaxRows
	page := u.CourseScroll
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * CourseMaxRows
	end := start + CourseMaxRows
	if end > len(tt.Courses) {
		end = len(tt.Courses)
	}

	for i := start; i < end; i++ {
		row := i - start
		c := tt.Courses[i]
		rowY := CoursesStartY + row*CourseRowH
		isNext := i == snap.NextCourseIdx

		if isNext {
			FillRect(img, RightX, rowY+8, 4, CourseRowH-16, ColorBlack)
		}

		baseline := baselineIn(r.faceCourseSm, rowY, CourseRowH)
		x := RightX + 12
		Text(img, r.faceCourseSm, c.StartText, x, baseline, ColorGray)
		x += CourseTimeW

		nameFace := r.faceCourseRow
		if isNext {
			nameFace = r.faceCourseRowBold
		}

		// 有教室时教室列固定占右端 CourseRoomW 的宽度，课程名不会忽长忽短；
		// 教师列插在课名与教室之间，宽度按实际文字（教师为空时不占位）。
		roomRight := RightX + RightW
		roomStart := roomRight
		if c.Room != "" {
			roomStart = roomRight - CourseRoomW
		}
		teacherW := 0
		if c.Teacher != "" {
			teacherW = MeasureString(r.faceCourseSm, c.Teacher)
			// 教师再长也要给课名留出至少 80px：课名才是这一行的主体。
			if maxW := roomStart - 8 - x - 80; teacherW > maxW {
				teacherW = maxW
			}
			if teacherW < 0 {
				teacherW = 0
			}
		}
		nameMaxW := roomStart - 8 - x
		if teacherW > 0 {
			nameMaxW -= teacherW + 8
		}
		if nameMaxW < 80 {
			nameMaxW = 80
		}
		Text(img, nameFace, Ellipsize(nameFace, c.Name, nameMaxW), x, baseline, ColorBlack)

		if teacherW > 0 {
			TextRight(img, r.faceCourseSm,
				Ellipsize(r.faceCourseSm, c.Teacher, teacherW),
				roomStart-8, baseline, ColorGray)
		}
		if c.Room != "" {
			TextRight(img, r.faceCourseSm, Ellipsize(r.faceCourseSm, c.Room, CourseRoomW),
				roomRight, baseline, ColorGray)
		}
	}

	// 页码只在多于一页时显示。
	//
	// 放在底部带的**左侧**（RightX+12）而不是右侧：右下角是电量百分比，
	// 两者会撞在一起。
	if pages > 1 {
		Text(img, r.faceCourseSm, fmt.Sprintf("%d/%d", page+1, pages), RightX+12,
			baselineIn(r.faceCourseSm, BatteryY+8, 40), ColorLight)
	}
}

// courseTitle 返回课程区标题。
//
// 选中别的日期时，标题依次带上完整农历与节假日名/节气：
//
//	10月30日 课程 · 九月廿二 · 国庆节
//
// 抽成纯函数而不是内联在 drawCourses：标题是多项信息的固定顺序拼接，
// 顺序错了在像素上极难看出来，单独测字符串才有判别力。
func courseTitle(snap state.Snapshot, u ui.State) string {
	if u.FollowToday() {
		return "今日课程"
	}
	parts := []string{fmt.Sprintf("%d月%d日 课程",
		int(u.SelectedDate.Month()), u.SelectedDate.Day())}
	if info, ok := snap.Calendar[u.SelectedDate.Format("2006-01-02")]; ok {
		if lunar := info.LunarFull(); lunar != "" {
			parts = append(parts, lunar)
		}
		if extra := selectedDayExtra(info); extra != "" {
			parts = append(parts, extra)
		}
	}
	return strings.Join(parts, " · ")
}

// selectedDayExtra 返回课程区标题后缀：先节假日名，没有就节气名。
//
// 用 Tag() 而不是 HolidayName：调休上班日的名字后面要带「调休」，
// 只显示「春节」会让人以为那天放假。
func selectedDayExtra(d state.DayInfo) string {
	if tag := d.Tag(); tag != "" {
		return tag
	}
	return d.SolarTerm
}

// drawBattery 在课程区底部右侧画电量百分比。
//
// 为什么放这儿：课程行最多 6 行到 y=1016，往下这条 56px 带里右侧是空白
// （左侧是翻页页码）；而且它已经在 courses 刷新区域内，
// 电量变化随该区域一起局部刷新，不用单开区域。
//
// 只画百分比，不画充电指示：设备 USB 常供电，status 几乎永远 Charging/Full，
// 这个指示没有信息量；Battery.Charging 字段留给将来用。
func (r *Renderer) drawBattery(img *image.Gray, snap state.Snapshot) {
	if !snap.Battery.Known {
		return
	}
	text := strconv.Itoa(snap.Battery.Pct) + "%"
	TextRight(img, r.faceBattery, text, RightX+RightW,
		baselineIn(r.faceBattery, BatteryY, 56), ColorLight)
}

// drawGear 画设置入口：一个淡淡的齿轮。
//
// 之前只有 hit.go 里的热区、没有绘制 —— 结果是「看不见但点得到」，用户
// 永远发现不了设置页。位置与 GearX/GearY 一致，始终绘制（与电量是否读得到
// 无关：设置入口不该因为电池驱动缺席而消失）。
func (r *Renderer) drawGear(img *image.Gray) {
	cx := GearX + GearW/2
	cy := GearY + GearH/2

	// 外圆填浅灰、内圆挖白 → 一个环。
	Circle(img, cx, cy, 13, ColorLight)
	Circle(img, cx, cy, 6, ColorWhite)
	// 四个轴向、每个方向两端各一段 4px 的短径向线当齿。
	for _, deg := range []float64{0, 45, 90, 135} {
		for _, dir := range []float64{-1, 1} {
			radialTick(img, cx, cy, deg, dir, 13, 17, ColorLight)
		}
	}
}

// radialTick 从半径 r0 画到 r1，沿 deg 方向（dir=-1 表示反方向）。
// 每个半径步画一个 2×2 的方块：1px 的斜线在电子墨水屏上太淡。
func radialTick(img *image.Gray, cx, cy int, deg, dir float64, r0, r1 int, c color.Color) {
	rad := deg * math.Pi / 180
	sin, cos := math.Sincos(rad)
	for rr := r0; rr <= r1; rr++ {
		x := cx + int(math.Round(dir*cos*float64(rr)))
		y := cy - int(math.Round(dir*sin*float64(rr)))
		FillRect(img, x-1, y-1, 2, 2, c)
	}
}

// drawFooter 画底部一行：左侧天气数据时间（含滞后标记），右侧教学周。
//
// 放在这里而不是 drawWeather 里，是因为这一行落在 footer 区（y 962..1002），
// 不属于 weather 区（540..868）。
func (r *Renderer) drawFooter(img *image.Gray, snap state.Snapshot) {
	baseline := baselineIn(r.faceDataTime, DataTimeY, 32)

	if w := snap.Weather; w != nil {
		label := "数据 " + w.FetchedAt.Format("15:04")
		if !snap.WeatherFresh {
			label += "（已过期）"
		}
		TextColumnLeft(img, r.faceDataTime, label, LeftX, baseline, ColorLight)
	}

	if snap.Timetable.Week > 0 {
		TextRight(img, r.faceDataTime, fmt.Sprintf("第 %d 教学周", snap.Timetable.Week),
			LeftX+LeftW, baseline, ColorLight)
	}
}

// ---- 辅助 ----

func weekdayCN(w time.Weekday) string {
	return [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}[w]
}

// sameDate 报告两个时间是否落在同一天 —— 只看年月日，不看时钟与位置。
// 格子日期与选中日期来自不同的构造路径，用 == 比较 time.Time 会因为
// 单调时钟读数和位置指针而误判。
func sameDate(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

func monthCN(m time.Month) string {
	return [...]string{"", "一月", "二月", "三月", "四月", "五月", "六月",
		"七月", "八月", "九月", "十月", "十一月", "十二月"}[m]
}

// drawWeatherIcon 按天气文本画一个方形矢量线稿，不依赖任何图标字体。
func drawWeatherIcon(img *image.Gray, x, y, size int, text string) {
	cx := x + size/2
	cy := y + size/2
	rad := size/2 - 4
	switch {
	case strings.Contains(text, "晴"):
		Circle(img, cx, cy, rad, ColorBlack)
		Circle(img, cx, cy, rad-10, ColorWhite)
	case strings.Contains(text, "云") || strings.Contains(text, "阴"):
		Circle(img, cx-14, cy+4, 16, ColorBlack)
		Circle(img, cx+12, cy-4, 20, ColorBlack)
		FillRect(img, cx-30, cy+2, 60, 18, ColorBlack)
		Circle(img, cx-14, cy+4, 11, ColorWhite)
		Circle(img, cx+12, cy-4, 15, ColorWhite)
		FillRect(img, cx-26, cy, 52, 12, ColorWhite)
	case strings.Contains(text, "雨"):
		Circle(img, cx, cy-16, 20, ColorBlack)
		FillRect(img, cx-20, cy-16, 40, 16, ColorBlack)
		for i := -1; i <= 1; i++ {
			FillRect(img, cx+i*16-2, cy+6, 4, 18, ColorBlack)
		}
	case strings.Contains(text, "雪"):
		Circle(img, cx, cy-16, 20, ColorBlack)
		FillRect(img, cx-20, cy-16, 40, 16, ColorBlack)
		for i := -1; i <= 1; i++ {
			Circle(img, cx+i*16, cy+12, 3, ColorBlack)
		}
	default:
		FillRect(img, cx-16, cy-16, 32, 32, ColorBlack)
		FillRect(img, cx-10, cy-10, 20, 20, ColorWhite)
	}
}
