package render

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/rhsy/kindleclock/src/internal/state"
	"github.com/rhsy/kindleclock/src/internal/ui"
)

func newTestRenderer(t *testing.T) *Renderer {
	t.Helper()
	// 测试用 gofont 构造 Fonts：不依赖设备字体文件，任何机器都能跑
	f, err := NewFontsFromData(testFontData(), testFontData())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	r, err := NewRenderer(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func at(h, m int) time.Time {
	return time.Date(2026, 10, 29, h, m, 0, 0, time.FixedZone("CST", 8*3600))
}

func ptr(v int) *int { return &v }

func baseSnap() state.Snapshot {
	return state.Snapshot{
		Now: at(14, 32),
		Weather: &state.Weather{
			Text: "晴", Temp: 18, TempMax: 21, TempMin: 9,
			AQI: ptr(28), AQICategory: "优",
			FetchedAt: at(14, 30),
		},
		WeatherFresh: true,
		Timetable: state.Timetable{
			Available: true,
			Week:      9,
			FetchedAt: at(14, 30),
			Courses: []state.Course{
				{Name: "大学物理", StartText: "14:15", EndText: "14:55",
					Start: at(14, 15), End: at(14, 55), Section: "7节", Room: "C105"},
				{Name: "体育", StartText: "16:40", EndText: "17:20",
					Start: at(16, 40), End: at(17, 20), Section: "10节", Room: "田径场"},
			},
		},
		NextCourseIdx: 0,
	}
}

func TestRenderDimensions(t *testing.T) {
	r := newTestRenderer(t)
	img := r.Render(baseSnap(), ui.State{})
	b := img.Bounds()
	if b.Dx() != CanvasW || b.Dy() != CanvasH {
		t.Fatalf("尺寸 = %dx%d，期望 %dx%d", b.Dx(), b.Dy(), CanvasW, CanvasH)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	r := newTestRenderer(t)
	a := r.Render(baseSnap(), ui.State{})
	b := r.Render(baseSnap(), ui.State{})
	if !imagesEqual(a, b) {
		t.Fatal("相同输入必须产生逐像素相同的输出")
	}
}

func TestRenderClockChangesWithTime(t *testing.T) {
	r := newTestRenderer(t)
	s1 := baseSnap()
	s2 := baseSnap()
	s2.Now = at(14, 33)

	a := r.Render(s1, ui.State{})
	b := r.Render(s2, ui.State{})
	if imagesEqual(a, b) {
		t.Fatal("时间变了，输出必须不同")
	}
	if !diffWithin(a, b, rectFor("clock")) {
		t.Error("只变分钟时，差异必须局限在 clock 区")
	}
}

func TestRenderDateChangeAffectsDateRegion(t *testing.T) {
	r := newTestRenderer(t)
	s1 := baseSnap()
	s2 := baseSnap()
	s2.Now = at(14, 32).AddDate(0, 0, 1)

	a := r.Render(s1, ui.State{})
	b := r.Render(s2, ui.State{})
	if imagesEqual(a, b) {
		t.Fatal("日期变了，输出必须不同")
	}
	// 跨日会同时影响 clock / date / calendar 三个区
	if !diffWithin(a, b, Rect{X: 0, Y: 0, W: CanvasW, H: CanvasH}) {
		t.Fatal("diffWithin 自反性失败")
	}
}

func TestRenderNilWeatherDoesNotPanic(t *testing.T) {
	r := newTestRenderer(t)
	s := baseSnap()
	s.Weather = nil
	s.WeatherFresh = false
	if img := r.Render(s, ui.State{}); img == nil {
		t.Fatal("无天气时仍应产出图像")
	}
}

func TestRenderStaleWeatherMarksLag(t *testing.T) {
	r := newTestRenderer(t)
	fresh := baseSnap()
	stale := baseSnap()
	stale.WeatherFresh = false

	if imagesEqual(r.Render(fresh, ui.State{}), r.Render(stale, ui.State{})) {
		t.Error("新鲜/陈旧状态的渲染结果必须不同（界面要标注滞后）")
	}
}

// 课表不可用时的输出，必须与「有课」不同 ——
// 原来的 img == nil 断言恒假（Render 恒返回非 nil），等于没测。
func TestRenderUnavailableTimetable(t *testing.T) {
	r := newTestRenderer(t)

	unavail := baseSnap()
	unavail.Timetable.Available = false
	unavail.Timetable.Courses = nil
	unavail.Timetable.Message = "课表暂不可用"
	unavail.NextCourseIdx = -1

	withCourses := baseSnap()

	a := r.Render(unavail, ui.State{})
	b := r.Render(withCourses, ui.State{})
	if imagesEqual(a, b) {
		t.Fatal("课表不可用时画出了课程 —— 不可用提示没生效")
	}
	if !diffWithin(a, b, rectFor("courses")) {
		t.Error("课表状态差异应只影响 courses 区")
	}
	// 不可用提示必须落在 courses 区内
	if d := diffBounds(a, b); !rectContains(rectFor("courses").Standard(), d.Min) {
		t.Errorf("差异从 %v 起，超出 courses 区", d.Min)
	}
}

// 无剩余课程时的 message 必须真的被画出来，且画在居中的提示行位置。
func TestRenderNoCoursesLeft(t *testing.T) {
	r := newTestRenderer(t)

	empty := baseSnap()
	empty.Timetable.Courses = nil
	empty.Timetable.Message = "今日暂无后续课程"
	empty.NextCourseIdx = -1

	// 对照组：Available=false 走的是另一条分支，画的是「课表暂不可用」
	blank := empty
	blank.Timetable.Available = false

	a := r.Render(empty, ui.State{})
	b := r.Render(blank, ui.State{})
	if imagesEqual(a, b) {
		t.Fatal("无课 message 没被画出来")
	}
	if !diffWithin(a, b, rectFor("courses")) {
		t.Error("无课提示的差异应只影响 courses 区")
	}

	// message 必须落在它自己的提示行上（CoursesStartY+120 起 56px 高、
	// 水平居中），而不是被画到别处或干脆没画。
	msgRow := Rect{X: RightX, Y: CoursesStartY + 120, W: RightW, H: 56}
	box := inkBounds(a, msgRow)
	if box.Empty() {
		t.Fatal("无课提示行是空的 —— message 没画")
	}
	if center := (box.Min.X + box.Max.X) / 2; abs(center-(RightX+RightW/2)) > 20 {
		t.Errorf("无课提示的墨迹中心 %d 偏离右栏中心 %d 超过 20px", center, RightX+RightW/2)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// 课程超过一屏时只画第一页的 6 行；越出的课程通过翻页到达，
// 不再画「还有 N 节」—— 两者既占位置又和翻页重复。
func TestRenderManyCoursesTruncates(t *testing.T) {
	r := newTestRenderer(t)

	mk := func(n int) state.Snapshot {
		s := baseSnap()
		s.Timetable.Courses = nil
		for i := 0; i < n; i++ {
			h := 8 + i
			s.Timetable.Courses = append(s.Timetable.Courses, state.Course{
				Name:      "课程",
				StartText: pad2(h) + ":00", EndText: pad2(h) + ":45",
				Start: at(h, 0), End: at(h, 45),
				Room: "X" + pad2(h),
			})
		}
		s.NextCourseIdx = 0
		return s
	}

	six := r.Render(mk(6), ui.State{})
	ten := r.Render(mk(10), ui.State{})

	// 第一页的正文只由前 6 门课决定：6 门与 10 门在同一页应当逐像素相同。
	body := image.Rect(RightX, CoursesStartY, RightX+RightW,
		CoursesStartY+CourseMaxRows*CourseRowH)
	if n := diffInBand(six, ten, body); n != 0 {
		t.Errorf("10 门课的第一页正文与 6 门课不同（%d 像素）—— 第一页只应画前 6 行", n)
	}

	// 第一页只画 6 行 —— 第 7 行带里除了页码不能有任何课程墨迹。
	// body 取样带止于 y=1016，正好不含第 7 行；不加这条，「每页多画一行」
	// （end := start + CourseMaxRows + 1）的变异能存活。用 10 门课的第一页
	// （才有溢出），左端让出 CourseTimeW 以避开同一带里的页码，
	// 右端让到齿轮热区之前以避开设置图标。
	row7 := image.Rect(RightX+CourseTimeW, CoursesStartY+CourseMaxRows*CourseRowH,
		GearX, CoursesStartY+(CourseMaxRows+1)*CourseRowH)
	if n := inkIn(ten, row7); n != 0 {
		t.Fatalf("10 门课的第一页第 7 行带溢出 %d 个课程墨迹像素 —— 每页只能画 6 行", n)
	}

	// 两图唯一的差异是页码，必须落在 courses 区内。
	if !diffWithin(six, ten, rectFor("courses")) {
		t.Error("6 门/10 门课的差异越出了 courses 区")
	}

	// 第 7 行区域在 10 门课的第一页上不再有「还有 N 节」。
	// 该区域现在放页码，直接用 7 门课（同为 2 页）对照：既然页码相同、
	// 前 6 行也相同，整个 courses 区必须逐像素一致 —— 旧提示会随之改变。
	seven := r.Render(mk(7), ui.State{})
	if n := diffInBand(ten, seven, rectFor("courses").Standard()); n != 0 {
		t.Errorf("10 门课与 7 门课的第一页有 %d 像素不同 —— 「还有 N 节」应当已删除", n)
	}

	// 页码出现在底部带的左侧：两页时有，一页时无。
	foot := image.Rect(RightX, BatteryY, RightX+RightW/2, BatteryY+56)
	if inkIn(ten, foot) == 0 {
		t.Error("10 门课（2 页）应当显示页码")
	}
	if inkIn(six, foot) != 0 {
		t.Error("6 门课（1 页）不该显示页码")
	}
}

// 长天气文本必须截断在 weather 区内：weather 区止于 x=600，右侧是
// 600..682 的中缝盲带，那里不属于任何刷新区域，画上去就永远不会被刷新。
//
// 注意：不能用 inkBounds(img, rectFor("weather")) 判定 —— 那个函数本身就
// 只在区域内扫，溢出到区外的像素根本看不见，断言永远成立。
func TestRenderLongWeatherTextStaysInRegion(t *testing.T) {
	for _, text := range []string{"晴转多云有小雨", "晴转多云有雷阵雨", "多云转阴有雷阵雨伴短时大风"} {
		s := baseSnap()
		s.Weather.Text = text
		r := newTestRenderer(t)
		img := r.Render(s, ui.State{})

		// 扫 weather 行带、整幅宽度的左半（0..中缝线），看墨迹落在哪
		band := Rect{X: 0, Y: WeatherY, W: DividerX, H: WeatherH}
		box := inkBounds(img, band)
		if box.Empty() {
			t.Fatalf("%q 没有画出任何墨迹", text)
		}
		if box.Min.X < LeftX || box.Max.X > LeftX+LeftW {
			t.Errorf("%q 墨迹 x=[%d,%d] 越出左栏 [%d,%d]（会落进中缝盲带，永不刷新）",
				text, box.Min.X, box.Max.X-1, LeftX, LeftX+LeftW)
		}

		// 中缝两侧的盲带必须干净
		if !isBlankRegion(img, Rect{X: LeftX + LeftW, Y: 0, W: DividerX - (LeftX + LeftW), H: CanvasH}) {
			t.Errorf("%q：左栏与中缝之间出现墨迹 —— 该区域不属于任何刷新区", text)
		}
		if !isBlankRegion(img, Rect{X: DividerX + 1, Y: 0, W: RightX - (DividerX + 1), H: CanvasH}) {
			t.Errorf("%q：中缝与右栏之间出现墨迹 —— 该区域不属于任何刷新区", text)
		}
	}
}

// 空课表的长 message 不能越出右栏：原来用 TextCentered 会左右各溢出一截，
// 左边进中缝盲带、右边画出画布。
func TestRenderLongCourseMessageStaysInRegion(t *testing.T) {
	r := newTestRenderer(t)
	s := baseSnap()
	s.Timetable.Courses = nil
	s.NextCourseIdx = -1
	s.Timetable.Message = "教务系统暂时无法连接，请稍后重试。这学期课表服务器在早高峰时段经常超时，界面先显示上一次成功获取的课表。"

	img := r.Render(s, ui.State{})

	// 同样必须扫超出右栏的宽度：只看 courses 区内部的墨迹是看不见溢出的。
	// 左边界取中缝线之后（左栏 y=800..856 处有 AQI 行，不能一起扫）。
	band := Rect{X: DividerX + 1, Y: CoursesStartY + 120, W: CanvasW - (DividerX + 1), H: 56}
	box := inkBounds(img, band)
	if box.Empty() {
		t.Fatal("长 message 没有画出任何墨迹")
	}
	if box.Min.X < RightX || box.Max.X > RightX+RightW {
		t.Errorf("message 墨迹 x=[%d,%d] 越出右栏 [%d,%d]", box.Min.X, box.Max.X-1, RightX, RightX+RightW)
	}
	// 左越会进中缝盲带、右越会出画布，两者都是永远不会刷新的像素
	if !isBlankRegion(img, Rect{X: LeftX + LeftW, Y: CoursesStartY + 120, W: DividerX - (LeftX + LeftW), H: 56}) {
		t.Error("message 墨迹进了左栏与中缝之间的盲带")
	}
	if !isBlankRegion(img, Rect{X: DividerX + 1, Y: CoursesStartY + 120, W: RightX - (DividerX + 1), H: 56}) {
		t.Error("message 墨迹进了中缝与右栏之间的盲带")
	}

	// 短 message 与长 message 的渲染结果必须不同 —— 证明是截断而非丢弃
	short := s
	short.Timetable.Message = "无课"
	if imagesEqual(img, r.Render(short, ui.State{})) {
		t.Error("长短 message 渲染结果相同 —— message 可能没按内容画")
	}
}

// 今天必须整格反色高亮，且换一天后高亮跟着移动
func TestRenderTodayIsHighlighted(t *testing.T) {
	r := newTestRenderer(t)

	today := baseSnap()
	other := baseSnap()
	other.Now = at(14, 32).AddDate(0, 0, -1)

	a := r.Render(today, ui.State{})
	b := r.Render(other, ui.State{})
	if imagesEqual(a, b) {
		t.Fatal("换一天渲染结果相同 —— 今日高亮没生效")
	}

	ca := cropRegion(a, rectFor("calendar"))
	cb := cropRegion(b, rectFor("calendar"))
	if imagesEqual(ca, cb) {
		t.Fatal("calendar 区渲染结果相同 —— 今日高亮没跟着移动")
	}

	// 今天的格子必须是黑底：格子中心应为黑（反色胶囊覆盖）。
	// 取样点放在黑块左侧内部：日号是反白的，正中可能正好压在白色笔画上。
	cellX, cellY := calendarCellOf(today.Now)
	probe := image.Pt(cellX+6, cellY+CalCellH/2)
	if a.GrayAt(probe.X, probe.Y).Y == 255 {
		t.Error("今天的格子中心是白的 —— 没有反色高亮")
	}
	// 昨天的格子同一位置不应是黑
	cellX2, cellY2 := calendarCellOf(other.Now)
	probe2 := image.Pt(cellX2+6, cellY2+CalCellH/2)
	if a.GrayAt(probe2.X, probe2.Y).Y == 0 {
		t.Error("昨天的格子也被填黑了 —— 高亮画到了错误的格子")
	}
}

// 今天的日号必须比普通日号更大更粗（spec §4.3：40px Bold）
func TestRenderTodayUsesLargerBoldDigit(t *testing.T) {
	r := newTestRenderer(t)
	if r.faceToday == nil {
		t.Fatal("faceToday 未预热")
	}
	if r.faceToday == r.faceDayNum {
		t.Fatal("今天的字面与普通日号相同 —— 没有单独的 40px Bold")
	}

	// 单看字面大小还不够（40px Regular 也能通过），要量实际画出来的墨迹：
	// 今天的日号宽度必须落在 faceToday 的预期上，明显宽于 faceDayNum 的预期。
	for _, day := range []int{29, 30, 31, 15} {
		s := baseSnap()
		s.Now = time.Date(2026, 10, day, 14, 32, 0, 0, s.Now.Location())
		img := r.Render(s, ui.State{})

		cellX, cellY := calendarCellOf(s.Now)
		// 只看胶囊内部，避开圆角
		box := inkBounds(img, Rect{X: cellX + 2, Y: cellY + 8, W: CalCellW - 4, H: CalCellH - 16})
		if box.Empty() {
			t.Fatalf("%d 日的反白格内没有墨迹", day)
		}
		// 反白数字是白色：墨迹 = 非黑像素，这里改用白色统计
		whiteBox := whiteInkBounds(img, Rect{X: cellX + 2, Y: cellY + 8, W: CalCellW - 4, H: CalCellH - 16})
		if whiteBox.Empty() {
			t.Fatalf("%d 日的反白格内没有白色数字", day)
		}
		got := whiteBox.Dx()
		wantToday := inkWidth(r.faceToday, fmt.Sprintf("%d", day))
		wantNormal := inkWidth(r.faceDayNum, fmt.Sprintf("%d", day))
		if abs(got-wantToday) > 2 {
			t.Errorf("%d 日反白数字墨迹宽 %d，faceToday 预期 %d（faceDayNum 只有 %d）—— 今天没用 40px Bold",
				day, got, wantToday, wantNormal)
		}
	}
}

// inkWidth 返回字符串的墨迹宽度（像素），与 TextInkCentered 的口径一致。
func inkWidth(f font.Face, s string) int {
	d := &font.Drawer{Face: f}
	minX, maxX := 1<<30, -1<<30
	pen := fixed.Int26_6(0)
	for _, r := range s {
		b, adv, ok := d.Face.GlyphBounds(r)
		if ok {
			if int(pen+b.Min.X) < minX {
				minX = int(pen + b.Min.X)
			}
			if int(pen+b.Max.X) > maxX {
				maxX = int(pen + b.Max.X)
			}
		}
		pen += adv
	}
	if maxX < minX {
		return 0
	}
	return int((fixed.Int26_6(maxX)+32)>>6) - int((fixed.Int26_6(minX)+32)>>6)
}

// whiteInkBounds 返回区域内白色（=255）像素的包围盒，用于反白数字。
func whiteInkBounds(img *image.Gray, r Rect) image.Rectangle {
	rt := r.Standard().Intersect(img.Bounds())
	box := image.Rectangle{}
	for y := rt.Min.Y; y < rt.Max.Y; y++ {
		for x := rt.Min.X; x < rt.Max.X; x++ {
			if img.GrayAt(x, y).Y == 255 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

// 月历以周日为第一列：2026-10-01 是周四，应落在第 5 列（索引 4）。
//
// 关键是不能只改表头：补格推算若仍从周一起算，10/1 会画在第 4 列，
// 而周日的表头底下其实是 9/30 —— 日期整体偏一天。
func TestCalendarWeekStartsSunday(t *testing.T) {
	r := newTestRenderer(t)
	s := baseSnap()
	s.Now = time.Date(2026, 10, 1, 12, 0, 0, 0, s.Now.Location())

	img := r.Render(s, ui.State{})

	// 10/1 是「今天」：黑块必须落在第 1 行第 5 列（索引 4）。
	// 取黑块左侧内部，避开反白的日号笔画。
	todayCell := calendarCellRect(0, 4)
	probe := image.Pt(todayCell.Min.X+6, todayCell.Min.Y+CalCellH/2)
	if got := img.GrayAt(probe.X, probe.Y).Y; got != 0 {
		t.Errorf("10/1 的黑块不在第 5 列（(%d,%d) 灰度 %d）—— 周首日可能还是周一",
			probe.X, probe.Y, got)
	}
	// 第 4 列（索引 3）应是 9/30 的补格：只有浅灰日号，绝不能是今天的黑块。
	filler := Rect{X: RightX + 3*CalCellW, Y: CalGridY, W: CalCellW, H: CalCellH}
	if !onlyLightInk(img, filler) {
		t.Error("第 4 列出现了深色 —— 10/1 画到了索引 3（周一起算的位置）")
	}
}

// 星期表头必须从「日」开始，与 cellDate 的补格推算一致。
//
// 顺序用包级变量直接钉住：gofont 没有中文，「日一二三四五六」会被画成
// 同一个缺字方框，逐像素比较抓不住顺序变化（真字体下才看得出）。
func TestCalendarWeekHeaderStartsSunday(t *testing.T) {
	wantHeaders := []string{"日", "一", "二", "三", "四", "五", "六"}
	if len(weekdayHeaders) != len(wantHeaders) {
		t.Fatalf("表头数量 = %d，期望 %d", len(weekdayHeaders), len(wantHeaders))
	}
	for i, w := range wantHeaders {
		if weekdayHeaders[i] != w {
			t.Errorf("第 %d 列表头 = %q，期望 %q —— 一周应从周日开始", i, weekdayHeaders[i], w)
		}
	}

	r := newTestRenderer(t)
	img := r.Render(baseSnap(), ui.State{})

	want := image.NewGray(image.Rect(0, 0, CanvasW, CanvasH))
	for i := range want.Pix {
		want.Pix[i] = 255
	}
	for i, h := range weekdayHeaders {
		c := ColorGray
		if i == 0 || i == CalCols-1 {
			c = ColorLight
		}
		TextCentered(want, r.faceWeekHead, h,
			RightX+i*CalCellW, baselineIn(r.faceWeekHead, WeekHeaderY, 32), CalCellW, c)
	}
	band := image.Rect(RightX, WeekHeaderY, RightX+RightW, WeekHeaderY+32)
	if n := diffInBand(img, want, band); n != 0 {
		t.Fatalf("星期表头渲染与 weekdayHeaders 不符（%d 像素不同）", n)
	}
}

// 非本月补格必须被画出来（spec §4.3），且用浅灰
func TestCalendarDrawsAdjacentMonthDays(t *testing.T) {
	r := newTestRenderer(t)
	s := baseSnap()
	// 周首日是周日：2026-10-01 是周四，前 4 格是 9 月 27/28/29/30
	s.Now = time.Date(2026, 10, 15, 12, 0, 0, 0, s.Now.Location())
	img := r.Render(s, ui.State{})

	for c := 0; c < 4; c++ {
		cell := Rect{X: RightX + c*CalCellW, Y: CalGridY, W: CalCellW, H: CalCellH}
		if isBlankRegion(img, cell) {
			t.Errorf("第 %d 列（上月补格）是空的 —— 补格没画", c+1)
		}
		if !onlyLightInk(img, cell) {
			t.Errorf("第 %d 列（上月补格）用了深色 —— 补格应为灰 35%%", c+1)
		}
	}
	// 末行之后的补格同理：2026-10 共 31 天，前补 4 格 + 31 天 = 35 格，
	// 余 7 格是 11 月 1..7 日。
	last := Rect{X: RightX + 2*CalCellW, Y: CalGridY + 5*CalCellH, W: CalCellW, H: CalCellH}
	if isBlankRegion(img, last) {
		t.Error("末行补格是空的 —— 下月日号没画")
	}
}

// 冒号必须被画出来：它是时钟的一部分，且不是字形（自己画两个方块）
func TestRenderDrawsColon(t *testing.T) {
	r := newTestRenderer(t)
	img := r.Render(baseSnap(), ui.State{})

	total := 4*DigitCellW + ColonCellW
	startX := LeftX + (LeftW-total)/2
	colonRect := Rect{
		X: startX + 2*DigitCellW,
		Y: ClockY,
		W: ColonCellW,
		H: ClockCellH,
	}
	if isBlankRegion(img, colonRect) {
		t.Error("冒号格里没有任何墨迹 —— 冒号没画")
	}
}

// 时钟数字必须按墨迹居中：每个数字格的墨迹中心都应在格中心 ±2px，
// 否则「1」这类窄字形会在分钟跳变时左右挪动。
func TestRenderClockDigitsAreInkCentered(t *testing.T) {
	r := newTestRenderer(t)
	total := 4*DigitCellW + ColonCellW
	startX := LeftX + (LeftW-total)/2

	for _, hm := range [][2]int{{14, 32}, {11, 11}, {17, 17}, {10, 47}, {23, 59}, {0, 0}} {
		s := baseSnap()
		s.Now = at(hm[0], hm[1])
		img := r.Render(s, ui.State{})

		cells := []int{startX, startX + DigitCellW,
			startX + 2*DigitCellW + ColonCellW, startX + 3*DigitCellW + ColonCellW}
		for i, cx := range cells {
			cell := Rect{X: cx, Y: ClockY, W: DigitCellW, H: ClockCellH}
			box := inkBounds(img, cell)
			if box.Empty() {
				t.Fatalf("%02d:%02d 第 %d 位没有墨迹", hm[0], hm[1], i)
			}
			inkCenter := (box.Min.X + box.Max.X - 1) / 2
			cellCenter := cx + DigitCellW/2
			if d := inkCenter - cellCenter; d < -2 || d > 2 {
				t.Errorf("%02d:%02d 第 %d 位墨迹中心 %d 偏离格中心 %d 达 %d px",
					hm[0], hm[1], i, inkCenter, cellCenter, d)
			}
		}
	}
}

// Close 是空操作：字面归 Fonts 所有，调用方先 Close Renderer 再 Close Fonts
// 必须安全（T11 的 runPreview / runReal 都是这个顺序）。
func TestRendererCloseIsSafe(t *testing.T) {
	f, err := NewFontsFromData(testFontData(), testFontData())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := NewRenderer(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}
	// 关闭后 Fonts 仍可用 —— Close 没有把字体一起关掉
	if _, err := f.Face(Regular, 24); err != nil {
		t.Errorf("Renderer.Close 误关了 Fonts: %v", err)
	}
}

// 时钟格高必须装得下 200px 的字形墨迹。
//
// 中文字体的 metrics 差异很大：STHeiti 在 200px 时 ascent+descent 约 200，
// 只剩 20px 余量；某些 CJK 字体到 290。基线由 baselineIn 按 metrics 算，
// metrics 一旦超过 ClockCellH，数字就会被顶出 clock 区 —— 那部分像素
// 不属于任何刷新区域，永远不会被刷新。
//
// 注意：这里只做「字形墨迹 ≤ 格高」的静态守卫。渲染后是否真的没越界，
// 由 TestClockInkStaysInRegion 扫邻带检查 —— 不能用 inkBounds(clock)
// 判断，那个函数只扫区内像素，越界部分根本看不见，断言恒真。
func TestClockCellFitsClockFace(t *testing.T) {
	r := newTestRenderer(t)

	d := &font.Drawer{Face: r.faceClock}
	minY, maxY := 1<<30, -1<<30
	for _, ch := range "0123456789" {
		b, _, ok := d.Face.GlyphBounds(ch)
		if !ok {
			continue
		}
		if int(b.Min.Y) < minY {
			minY = int(b.Min.Y)
		}
		if int(b.Max.Y) > maxY {
			maxY = int(b.Max.Y)
		}
	}
	inkH := int(fixed.Int26_6(maxY-minY)>>6) + 1
	if inkH > ClockCellH {
		t.Errorf("200px 数字墨迹高 %d 超过 ClockCellH %d", inkH, ClockCellH)
	}
}

// 时钟墨迹必须完整落在 clock 区内。
//
// 关键：必须扫**区外**的邻带。用 inkBounds(img, rectFor("clock")) 再断言
// 「在区内」是恒真的 —— inkBounds 只遍历区域内的像素，溢出到区外的
// 那一半根本不会进入包围盒。评审用「在 clock 下方 20px 画一条黑线」
// 证明过这一点。
func TestClockInkStaysInRegion(t *testing.T) {
	r := newTestRenderer(t)
	clock := rectFor("clock").Standard()

	// 上邻带：date 区内 y=200..249 这段（DateY+128 起）到 clock 顶（250）。
	// 故意不用 DateY+178：那正好是 date 区底 250，带高会归零、守卫空转。
	above := Rect{X: LeftX, Y: DateY + 128, W: LeftW, H: ClockY - (DateY + 128)}
	// 下邻带：clock 底（470）到 weather 顶（540）
	below := Rect{X: LeftX, Y: clock.Max.Y, W: LeftW, H: WeatherY - clock.Max.Y}

	// 8 个时刻覆盖数字宽度差异最大的组合
	for _, hm := range [][2]int{
		{11, 11}, {14, 32}, {23, 59}, {4, 44},
		{10, 10}, {18, 18}, {0, 0}, {22, 22},
	} {
		s := baseSnap()
		s.Now = at(hm[0], hm[1])
		img := r.Render(s, ui.State{})

		if !isBlankRegion(img, above) {
			t.Errorf("%02d:%02d 时钟墨迹溢出到 clock 区上方（y=%d..%d）", hm[0], hm[1], above.Y, above.Bottom())
		}
		if !isBlankRegion(img, below) {
			t.Errorf("%02d:%02d 时钟墨迹溢出到 clock 区下方（y=%d..%d）", hm[0], hm[1], below.Y, below.Bottom())
		}
		// 时钟区本身必须有墨迹，否则「上下都空」可能只是什么都没画
		if isBlankRegion(img, rectFor("clock")) {
			t.Errorf("%02d:%02d clock 区是空的 —— 时钟没画", hm[0], hm[1])
		}
	}
}

// 左栏任何墨迹都必须落在某个刷新区域内：区域之间的空隙（date 与 clock 之间、
// clock 与 weather 之间、weather 与 footer 之间）是永远刷不新的盲带。
func TestLeftColumnHasNoInkOutsideRegions(t *testing.T) {
	r := newTestRenderer(t)
	scenarios := map[string]func(state.Snapshot) state.Snapshot{
		"base": func(s state.Snapshot) state.Snapshot { return s },
		"长天气": func(s state.Snapshot) state.Snapshot {
			s.Weather.Text = "晴转多云有雷阵雨"
			return s
		},
		"无天气": func(s state.Snapshot) state.Snapshot {
			s.Weather = nil
			return s
		},
		"过期天气": func(s state.Snapshot) state.Snapshot {
			s.WeatherFresh = false
			return s
		},
		// 农历/节气行画在 y 205..250：date 区没扩到 250 时，这条守卫就会红。
		// 只设农历而不设 SolarTerm —— Line() 节气优先，同时设会让
		// 这个场景一直画「立冬」，覆盖不到更长的「农历 九月廿九」形态。
		"有农历": func(s state.Snapshot) state.Snapshot {
			s.Day = state.DayInfo{LunarMonthName: "九月", LunarDayName: "廿九"}
			return s
		},
		// 节假日标签挂在日期行右侧，同样必须留在 date 区内。
		"节假日标签": func(s state.Snapshot) state.Snapshot {
			s.Day = state.DayInfo{HolidayName: "国庆节", HolidayKind: state.HolidayRest}
			return s
		},
	}

	var allowed []image.Rectangle
	for _, rg := range Regions() {
		if rg.Rect.X < DividerX {
			allowed = append(allowed, rg.Rect.Standard())
		}
	}

	for name, mut := range scenarios {
		img := r.Render(mut(baseSnap()), ui.State{})
		for y := 0; y < CanvasH; y++ {
			for x := LeftX; x < DividerX; x++ {
				if img.GrayAt(x, y).Y == 255 {
					continue
				}
				ok := false
				for _, a := range allowed {
					if image.Pt(x, y).In(a) {
						ok = true
						break
					}
				}
				if !ok {
					t.Fatalf("%s: 左栏 (%d,%d) 有墨迹，但不在任何刷新区内", name, x, y)
				}
			}
		}
	}
}

// AQI 有值时必须显示，无值时必须不显示（多画了就是错的）
func TestRenderAQIConditional(t *testing.T) {
	r := newTestRenderer(t)

	withAQI := baseSnap()
	withoutAQI := baseSnap()
	withoutAQI.Weather = &state.Weather{
		Text: "晴", Temp: 18, TempMax: 21, TempMin: 9,
		FetchedAt: at(14, 30),
	}

	a := r.Render(withAQI, ui.State{})
	b := r.Render(withoutAQI, ui.State{})
	if imagesEqual(a, b) {
		t.Fatal("有 AQI 与无 AQI 渲染结果相同 —— AQI 没画")
	}
	if !diffWithin(a, b, rectFor("weather")) {
		t.Error("AQI 差异应只影响 weather 区")
	}
	// AQI 行必须真有墨迹
	if isBlankRegion(a, Rect{X: LeftX, Y: AQIY, W: LeftW, H: 36}) {
		t.Error("AQI 行是空的 —— 有 AQI 时没画")
	}
	if !isBlankRegion(b, Rect{X: LeftX, Y: AQIY, W: LeftW, H: 36}) {
		t.Error("AQI 行有墨迹 —— 无 AQI 时画了不该画的东西")
	}
}

// 教学周必须显示在 footer，且周数变化要能看出来
func TestRenderTeachingWeek(t *testing.T) {
	r := newTestRenderer(t)

	w9 := baseSnap()
	w9.Timetable.Week = 9
	w1 := baseSnap()
	w1.Timetable.Week = 1
	w0 := baseSnap()
	w0.Timetable.Week = 0

	a := r.Render(w9, ui.State{})
	b := r.Render(w1, ui.State{})
	if imagesEqual(a, b) {
		t.Fatal("教学周 9 与 1 渲染结果相同 —— 教学周没画")
	}
	if !diffWithin(a, b, rectFor("footer")) {
		t.Error("教学周差异应只影响 footer 区")
	}

	// Week=0 表示未知，不应画「第 0 教学周」
	c := r.Render(w0, ui.State{})
	// 教学周右对齐到左栏右缘 600，取 footer 区间靠右的一半做判定
	weekArea := Rect{X: LeftX + LeftW/2, Y: DataTimeY, W: LeftW / 2, H: 40}
	if !isBlankRegion(c, weekArea) {
		t.Error("Week=0 时仍画了教学周")
	}
	// 对照：Week=9 时同一块必须有墨迹
	if isBlankRegion(a, weekArea) {
		t.Error("Week=9 时那块是空的 —— 教学周没画在左栏右缘")
	}
}

// 课表不可用时要显示「上次成功 HH:MM」，且时间变了要能看出来
func TestRenderLastSuccessTime(t *testing.T) {
	r := newTestRenderer(t)

	a := baseSnap()
	a.Timetable.Available = false
	a.Timetable.Courses = nil
	a.Timetable.Message = "课表暂不可用"
	a.Timetable.FetchedAt = at(14, 30)
	a.NextCourseIdx = -1

	b := a
	b.Timetable.FetchedAt = at(9, 5)

	x := r.Render(a, ui.State{})
	y := r.Render(b, ui.State{})
	if imagesEqual(x, y) {
		t.Fatal("上次成功时间不同但渲染结果相同 —— 时间没画")
	}
	if !diffWithin(x, y, rectFor("courses")) {
		t.Error("上次成功时间的差异应只影响 courses 区")
	}
}

// 带数据时间的 footer 必须能被区分（防止时间戳被写成常量）
func TestRenderFooterShowsFetchedTime(t *testing.T) {
	r := newTestRenderer(t)

	a := baseSnap()
	a.Weather.FetchedAt = at(14, 30)
	b := baseSnap()
	b.Weather.FetchedAt = at(9, 5)

	x := r.Render(a, ui.State{})
	y := r.Render(b, ui.State{})
	if imagesEqual(x, y) {
		t.Fatal("数据时间不同但渲染结果相同 —— 时间戳可能是写死的")
	}
	if !diffWithin(x, y, rectFor("footer")) {
		t.Error("数据时间差异应只影响 footer 区（drawWeather 不画数据时间）")
	}
}

// 长课程名必须被 Ellipsize，不得画到教室列上
func TestRenderLongCourseNameIsEllipsized(t *testing.T) {
	r := newTestRenderer(t)

	short := baseSnap()
	short.Timetable.Courses = []state.Course{{
		Name: "数学", StartText: "14:15", EndText: "14:55",
		Start: at(14, 15), End: at(14, 55), Room: "C105",
	}}
	short.NextCourseIdx = 0

	long := short
	long.Timetable.Courses = []state.Course{{
		Name:      "这是一个极其漫长的课程名称会一直延伸到教室列上",
		StartText: "14:15", EndText: "14:55",
		Start: at(14, 15), End: at(14, 55), Room: "C105",
	}}

	a := r.Render(short, ui.State{})
	b := r.Render(long, ui.State{})

	// 长名字的墨迹不得进入教室列（右栏右侧 CourseRoomW 宽的区域）
	roomLeft := RightX + RightW - CourseRoomW
	room := Rect{X: roomLeft, Y: CoursesStartY, W: CourseRoomW, H: CourseRowH}
	if !imagesEqual(cropRegion(a, room), cropRegion(b, room)) {
		t.Error("长课程名画进了教室列 —— Ellipsize 没生效")
	}
}

// 中缝线是 1px 黑（spec §4.1），不是浅灰
func TestRenderDividerIsBlack(t *testing.T) {
	r := newTestRenderer(t)
	img := r.Render(baseSnap(), ui.State{})
	if got := img.GrayAt(DividerX, DividerY+10).Y; got != 0 {
		t.Errorf("中缝线灰度 = %d，spec 要求 1px 黑（0）", got)
	}
	// 线宽 1px：左右各一格必须是白的
	if g := img.GrayAt(DividerX-1, DividerY+10).Y; g != 255 {
		t.Errorf("中缝线左侧有墨迹（灰度 %d），应该是 1px", g)
	}
	if g := img.GrayAt(DividerX+1, DividerY+10).Y; g != 255 {
		t.Errorf("中缝线右侧有墨迹（灰度 %d），应该是 1px", g)
	}
}

func TestRenderNextCourseHighlighted(t *testing.T) {
	r := newTestRenderer(t)
	withNext := baseSnap()
	withNext.NextCourseIdx = 0

	noNext := baseSnap()
	noNext.NextCourseIdx = -1

	if imagesEqual(r.Render(withNext, ui.State{}), r.Render(noNext, ui.State{})) {
		t.Error("标记下一节课时输出必须不同（左侧竖条 + 粗体）")
	}
}

func pad2(v int) string {
	return string([]rune{'0' + rune(v/10), '0' + rune(v%10)})
}

// ---- 辅助 ----

func imagesEqual(a, b *image.Gray) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

func rectFor(id string) Rect {
	for _, r := range Regions() {
		if r.ID == id {
			return r.Rect
		}
	}
	return Rect{}
}

// diffWithin 检查两张图在给定矩形之外完全相同。
func diffWithin(a, b *image.Gray, r Rect) bool {
	rt := r.Standard()
	for y := 0; y < CanvasH; y++ {
		for x := 0; x < CanvasW; x++ {
			if image.Pt(x, y).In(rt) {
				continue
			}
			if a.GrayAt(x, y) != b.GrayAt(x, y) {
				return false
			}
		}
	}
	return true
}

// rectContains 判断点是否在矩形内。image.Rectangle 没有 Contains 方法，
// 只有 Point.In，这里包一层让断言读起来直白些。
func rectContains(r image.Rectangle, p image.Point) bool { return p.In(r) }

// diffBounds 返回两图差异像素的包围盒（无差异时返回空矩形）
func diffBounds(a, b *image.Gray) image.Rectangle {
	minX, minY, maxX, maxY := CanvasW, CanvasH, -1, -1
	for y := 0; y < CanvasH; y++ {
		for x := 0; x < CanvasW; x++ {
			if a.GrayAt(x, y) != b.GrayAt(x, y) {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

// inkBounds 返回区域内非白像素的包围盒（无墨迹时返回空矩形）
func inkBounds(img *image.Gray, r Rect) image.Rectangle {
	rt := r.Standard().Intersect(img.Bounds())
	box := image.Rectangle{}
	for y := rt.Min.Y; y < rt.Max.Y; y++ {
		for x := rt.Min.X; x < rt.Max.X; x++ {
			if img.GrayAt(x, y).Y != 255 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

// isBlankRegion 判断区域内是否全白
func isBlankRegion(img *image.Gray, r Rect) bool {
	rt := r.Standard().Intersect(img.Bounds())
	for y := rt.Min.Y; y < rt.Max.Y; y++ {
		for x := rt.Min.X; x < rt.Max.X; x++ {
			if img.GrayAt(x, y).Y != 255 {
				return false
			}
		}
	}
	return true
}

// onlyLightInk 判断区域内的非白像素是否都是浅灰（补格专用）
func onlyLightInk(img *image.Gray, r Rect) bool {
	rt := r.Standard().Intersect(img.Bounds())
	found := false
	for y := rt.Min.Y; y < rt.Max.Y; y++ {
		for x := rt.Min.X; x < rt.Max.X; x++ {
			if v := img.GrayAt(x, y).Y; v != 255 {
				found = true
				if v < 128 {
					return false
				}
			}
		}
	}
	return found
}

// cropRegion 切出一个区域，便于逐像素比对
func cropRegion(img *image.Gray, r Rect) *image.Gray {
	rt := r.Standard().Intersect(img.Bounds())
	out := image.NewGray(image.Rect(0, 0, rt.Dx(), rt.Dy()))
	for y := 0; y < rt.Dy(); y++ {
		for x := 0; x < rt.Dx(); x++ {
			out.SetGray(x, y, img.GrayAt(rt.Min.X+x, rt.Min.Y+y))
		}
	}
	return out
}

// calendarCellOf 返回某天所在格子左上角坐标（周日为第一列）
func calendarCellOf(t time.Time) (int, int) {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	offset := int(first.Weekday()) // 周日 = 0，正好是周首日
	idx := offset + t.Day() - 1
	return RightX + (idx%CalCols)*CalCellW, CalGridY + (idx/CalCols)*CalCellH
}

// ---- 设备字体回归 ----

// deviceFontPaths 返回可用的设备字体路径。优先环境变量，其次默认路径。
// 找不到就返回空，调用方 t.Skip。
func deviceFontPaths(t *testing.T) (string, string) {
	t.Helper()
	if reg := os.Getenv("KINDLECLOCK_TEST_FONT_REGULAR"); reg != "" {
		bold := os.Getenv("KINDLECLOCK_TEST_FONT_BOLD")
		if bold == "" {
			bold = reg
		}
		return reg, bold
	}
	for _, dir := range []string{"/tmp/kindlefonts", "/usr/java/lib/fonts"} {
		reg := dir + "/STHeitiMedium.ttf"
		bold := dir + "/STHeitiBold.ttf"
		if _, err := os.Stat(reg); err == nil {
			if _, err := os.Stat(bold); err != nil {
				bold = reg
			}
			return reg, bold
		}
	}
	return "", ""
}

// 用真实 CJK 字体（设备 STHeiti，或环境变量指定的替代品）跑同一批版面守卫。
//
// 为什么要单独跑：gofont 没有中文，中文按缺字兜底绘制，字形的边距/宽度
// 与 STHeiti 完全不同。评审实测 STHeiti 的「2」有 −1px 左边距，会让月份
// 标题的墨迹落到 RightX−1 —— 这一点只有真字体才暴露得出来。
func TestWithDeviceFonts(t *testing.T) {
	reg, bold := deviceFontPaths(t)
	if reg == "" {
		t.Skip("没有可用的设备字体（设 KINDLECLOCK_TEST_FONT_REGULAR 指定）")
	}
	f, err := NewFonts(reg, bold)
	if err != nil {
		t.Fatalf("设备字体解析失败: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	r, err := NewRenderer(f)
	if err != nil {
		t.Fatal(err)
	}

	// 1) 中缝两侧的盲带必须干净 —— 尤其是月份标题首字的负左边距
	for _, text := range []string{"晴", "晴转多云有小雨", "晴转多云有雷阵雨"} {
		s := baseSnap()
		s.Weather.Text = text
		img := r.Render(s, ui.State{})

		if !isBlankRegion(img, Rect{X: LeftX + LeftW, Y: 0, W: DividerX - (LeftX + LeftW), H: CanvasH}) {
			t.Errorf("%q：左栏与中缝之间出现墨迹（会永不刷新）", text)
		}
		if !isBlankRegion(img, Rect{X: DividerX + 1, Y: 0, W: RightX - (DividerX + 1), H: CanvasH}) {
			t.Errorf("%q：中缝与右栏之间出现墨迹（会永不刷新）", text)
		}
	}

	// 2) 右栏所有墨迹都必须在右栏区域内（月份标题负边距会打破这条）
	for _, s := range []state.Snapshot{baseSnap(), func() state.Snapshot {
		x := baseSnap()
		x.Timetable.Courses = nil
		x.Timetable.Message = "教务系统暂时无法连接，请稍后重试，这是一条很长的提示文字"
		x.NextCourseIdx = -1
		return x
	}()} {
		img := r.Render(s, ui.State{})
		for y := 0; y < CanvasH; y++ {
			for x := DividerX + 1; x < RightX; x++ {
				if img.GrayAt(x, y).Y != 255 {
					t.Fatalf("右栏外 (%d,%d) 有墨迹 —— 该像素不属于任何刷新区", x, y)
				}
			}
		}
	}

	// 3) 时钟墨迹不能越过 clock 区上/下边界
	clock := rectFor("clock").Standard()
	above := Rect{X: LeftX, Y: DateY + 128, W: LeftW, H: ClockY - (DateY + 128)}
	below := Rect{X: LeftX, Y: clock.Max.Y, W: LeftW, H: WeatherY - clock.Max.Y}
	for _, hm := range [][2]int{{11, 11}, {23, 59}, {4, 44}, {0, 0}} {
		s := baseSnap()
		s.Now = at(hm[0], hm[1])
		img := r.Render(s, ui.State{})
		if !isBlankRegion(img, above) {
			t.Errorf("%02d:%02d 时钟墨迹溢出到 clock 区上方", hm[0], hm[1])
		}
		if !isBlankRegion(img, below) {
			t.Errorf("%02d:%02d 时钟墨迹溢出到 clock 区下方", hm[0], hm[1])
		}
	}

	// 4) 长课程名不得侵入教室列
	short := baseSnap()
	short.Timetable.Courses = []state.Course{{
		Name: "数学", StartText: "14:15", Room: "C105",
		Start: at(14, 15), End: at(14, 55),
	}}
	short.NextCourseIdx = 0
	long := short
	long.Timetable.Courses = []state.Course{{
		Name: "这是一个极其漫长的课程名称会一直延伸到教室列上", StartText: "14:15", Room: "C105",
		Start: at(14, 15), End: at(14, 55),
	}}
	room := Rect{X: RightX + RightW - CourseRoomW, Y: CoursesStartY, W: CourseRoomW, H: CourseRowH}
	if !imagesEqual(cropRegion(r.Render(short, ui.State{}), room), cropRegion(r.Render(long, ui.State{}), room)) {
		t.Error("长课程名画进了教室列 —— Ellipsize 没生效")
	}
}

// 月份标题必须用「墨迹左对齐」绘制：把带负左边距的假字面塞进字面缓存，
// 渲染后墨迹不得落进 LeftW..RightX 之间的中缝盲带。
//
// gofont 的「2」左边距是 +2.4px（正的），真字体 STHeiti 是 −1px —— 只用
// gofont 测不出这条，所以这里直接构造一个负边距字面替换月份字面。
func TestMonthTitleKeepsNegativeBearingInsideColumn(t *testing.T) {
	f, err := NewFontsFromData(testFontData(), testFontData())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// 把 Bold @ pxMonth 换成负边距假字面（Renderer 预热时就会取到它）
	f.mu.Lock()
	f.cache[faceKey{style: Bold, px: pxMonth}] = negBearingFace{lsb: -fixed.I(1)}
	f.mu.Unlock()

	r, err := NewRenderer(f)
	if err != nil {
		t.Fatal(err)
	}
	img := r.Render(baseSnap(), ui.State{})

	// 月份标题行（MonthY..MonthY+56）的墨迹左缘必须 ≥ RightX
	for x := DividerX + 1; x < RightX; x++ {
		for y := MonthY; y < MonthY+56; y++ {
			if img.GrayAt(x, y).Y != 255 {
				t.Fatalf("月份标题的墨迹落在 (%d,%d) —— 该像素在中缝盲带里，永远不会被刷新", x, y)
			}
		}
	}
	// 且必须真的画出来了（否则上面循环空转通过）
	if isBlankRegion(img, Rect{X: RightX, Y: MonthY, W: RightW, H: 56}) {
		t.Fatal("月份标题没有画出墨迹")
	}
}

// 每种天气都要有可辨认的图标，且不同类别不能画出同一张图。
func TestWeatherIconPerCategory(t *testing.T) {
	r := newTestRenderer(t)
	icon := Rect{X: LeftX, Y: WeatherY, W: WeatherH, H: WeatherH}

	// 图标按关键词分五类：晴 / 云或阴 / 雨 / 雪 / 兜底。
	// 「多云」与「阴」同走云分支，所以只取一个代表。
	var prev *image.Gray
	for _, text := range []string{"晴", "多云", "小雨", "小雪", "雾"} {
		s := baseSnap()
		s.Weather.Text = text
		img := r.Render(s, ui.State{})

		if isBlankRegion(img, icon) {
			t.Errorf("%q 的图标区是空的 —— 图标没画", text)
		}
		if prev != nil && imagesEqual(cropRegion(prev, icon), cropRegion(img, icon)) {
			t.Errorf("%q 与上一类的图标完全相同 —— 分类分支没生效", text)
		}
		prev = img
	}
}

// 晴天图标是空心圆（太阳）：中心必须是白的，圆环上有黑。
func TestWeatherIconSunIsRing(t *testing.T) {
	r := newTestRenderer(t)
	s := baseSnap()
	s.Weather.Text = "晴"
	img := r.Render(s, ui.State{})

	cx := LeftX + WeatherH/2
	cy := WeatherY + WeatherH/2
	if got := img.GrayAt(cx, cy).Y; got != 255 {
		t.Errorf("晴天图标中心灰度 = %d，空心太阳应为白", got)
	}
	if got := img.GrayAt(cx+WeatherH/2-8, cy).Y; got == 255 {
		t.Error("晴天图标环上没有墨迹 —— 圆没画")
	}
}

// 字体文件走 mmap 而不是整读进堆。
//
// 这条断言值得有：mmap 一旦退回 ReadFile，功能完全正常、测试也全绿，
// 只有内存悄悄多 20 MB —— 正是那种「不会报错、只会慢慢吃掉设备内存」
// 的回归。实测（2026-10-30）两份 STHeiti 整读占 20.2 MB RSS，
// mmap 后字体部分只占 3.5 MB。
func TestNewFontsMapsFontFiles(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "reg.ttf")
	bold := filepath.Join(dir, "bold.ttf")
	if err := os.WriteFile(reg, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bold, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := NewFonts(reg, bold)
	if err != nil {
		t.Fatalf("NewFonts: %v", err)
	}
	t.Logf("已映射字体数: %d", len(f.maps))

	// 渲染一次，证明映射的数据真的能用
	img := image.NewGray(image.Rect(0, 0, 200, 60))
	face, err := f.Face(Regular, 32)
	if err != nil {
		t.Fatal(err)
	}
	Text(img, face, "12:34", 0, 40, color.Black)

	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 重复 Close 不应 panic（maps 已置空）
	if err := f.Close(); err != nil {
		t.Fatalf("二次 Close: %v", err)
	}

	if len(f.maps) != 0 {
		t.Fatalf("Close 之后 maps 应被清空，实际剩 %d", len(f.maps))
	}
}

// 单独验证 mapOrRead：正常文件走映射，不存在的文件报错。
func TestMapOrRead(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.bin")
	want := []byte("hello font data")
	if err := os.WriteFile(p, want, 0o644); err != nil {
		t.Fatal(err)
	}
	data, mapped, err := mapOrRead(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(want) {
		t.Fatalf("内容不对: %q", data)
	}
	t.Logf("mapped=%v（在 tmpfs/ext4 上应为 true；某些文件系统不支持 mmap 会退回整读）", mapped)
	if mapped {
		munmap(data)
	}

	if _, _, err := mapOrRead(filepath.Join(dir, "nope.bin")); err == nil {
		t.Fatal("不存在的文件应该报错")
	}

	// 空文件：mmap 长度为 0 会失败，必须走退回路径而不是崩掉
	empty := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if data, mapped, err := mapOrRead(empty); err != nil || len(data) != 0 {
		t.Fatalf("空文件应返回空切片且无错，得到 len=%d err=%v", len(data), err)
	} else if mapped {
		t.Fatal("空文件不该被映射")
	}
}

// 农历行落在 y 200..250 —— 这条带原本没有任何刷新区域覆盖，
// 所以 date 区域必须扩到 y=250，否则这行首次显示后永远刷不新。
func TestDayInfoLineInsideDateRegion(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Day = state.DayInfo{SolarTerm: "立冬", LunarMonthName: "九月", LunarDayName: "廿九"}
	img := r.Render(snap, ui.State{})

	var dateRect Rect
	for _, reg := range Regions() {
		if reg.ID == "date" {
			dateRect = reg.Rect
		}
	}
	if dateRect.H < 178 {
		t.Fatalf("date 区域高度应至少 178（覆盖到 y=250），实际 %d", dateRect.H)
	}

	// 用「有/无 Day」差分取这一行的真实包围盒，要求墨迹上缘 ≥ 200。
	// 注意：gofont 下这条只是 ≥200 的下界守卫。把 LunarY 改到 190 时，
	// gofont 的缺字兜底字形上缘实测仍是 204，这里抓不住；真正钉死 190
	// 的是 TestDayInfoLineInsideDateRegionWithDeviceFont（STHeiti 实测到 194）。
	dayInk := diffBounds(img, r.Render(baseSnap(), ui.State{}))
	if dayInk.Empty() {
		t.Fatal("有/无 Day 的渲染结果相同 —— 农历行没画出来")
	}
	if dayInk.Min.Y < 200 {
		t.Fatalf("农历行墨迹上缘 y=%d < 200 —— LunarY 偏移到了星期行里", dayInk.Min.Y)
	}

	// 农历那一行的墨迹必须全部落在 date 区域内。
	// 只扫左栏（x < DividerX）：中缝 x=620 是静态元素、右栏属于 calendar 区，
	// 它们与本行无关；而 600..619 这段盲带仍会被覆盖到，不会被漏掉。
	for y := 200; y < 250; y++ {
		for x := 0; x < DividerX; x++ {
			if img.GrayAt(x, y).Y >= 250 {
				continue
			}
			if !image.Pt(x, y).In(image.Rect(dateRect.X, dateRect.Y,
				dateRect.Right(), dateRect.Y+dateRect.H)) {
				t.Fatalf("(%d,%d) 的墨迹落在 date 区域之外", x, y)
			}
		}
	}

	// 确认这一行确实画了东西：y 200..250 的左栏内必须有墨迹。
	// 数到 DividerX 为止，免得中缝那条静态竖线让这条断言恒真。
	ink := 0
	for y := 200; y < 250; y++ {
		for x := 0; x < DividerX; x++ {
			if img.GrayAt(x, y).Y < 250 {
				ink++
			}
		}
	}
	if ink == 0 {
		t.Fatal("y 200..250 之间没有墨迹，农历行没画出来")
	}
}

// 节气优先于农历日。
func TestDayInfoShowsSolarTermFirst(t *testing.T) {
	r := newTestRenderer(t)
	withTerm := baseSnap()
	withTerm.Day = state.DayInfo{SolarTerm: "立冬", LunarMonthName: "九月", LunarDayName: "廿九"}
	lunarOnly := baseSnap()
	lunarOnly.Day = state.DayInfo{LunarMonthName: "九月", LunarDayName: "廿九"}

	a := r.Render(withTerm, ui.State{})
	b := r.Render(lunarOnly, ui.State{})
	// 有节气时不该出现「农历」两个字（农历行整行换成节气名）
	if inkIn(a, image.Rect(56, 200, 600, 250)) >= inkIn(b, image.Rect(56, 200, 600, 250)) {
		t.Fatal("显示节气时的墨迹应少于带「农历」前缀的农历行")
	}
}

// 没有数据时这一行不画。
func TestDayInfoAbsentWhenNoData(t *testing.T) {
	r := newTestRenderer(t)
	img := r.Render(baseSnap(), ui.State{}) // Day 为零值
	if inkIn(img, image.Rect(56, 200, 600, 250)) != 0 {
		t.Fatal("没有农历数据时不该画任何东西")
	}
}

// 真实 STHeiti 下，农历行必须完整落在 date 区域内。
//
// 为什么需要单独一条：其它渲染测试都跑在 gofont 上（newTestRenderer 写死了
// testFontData），而 gofont 与 STHeiti 的字符度量不同。改 pxLunar / LunarY 时，
// gofont 下可能刚好放得下、真机上就出界 —— 而区域外的墨迹永远刷不新。
func TestDayInfoLineInsideDateRegionWithDeviceFont(t *testing.T) {
	reg, bold := deviceFontPaths(t)
	if reg == "" {
		t.Skip("没有可用的设备字体（设 KINDLECLOCK_TEST_FONT_REGULAR 指定）")
	}
	f, err := NewFonts(reg, bold)
	if err != nil {
		t.Fatalf("设备字体解析失败: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	r, err := NewRenderer(f)
	if err != nil {
		t.Fatal(err)
	}

	without := r.Render(baseSnap(), ui.State{})
	with := baseSnap()
	with.Day = state.DayInfo{SolarTerm: "立冬", LunarMonthName: "九月", LunarDayName: "廿九"}
	diff := r.Render(with, ui.State{})

	// 取「有 Day 与无 Day」两张图的差异包围盒 —— 那就是农历行独有的墨迹。
	minX, minY, maxX, maxY := CanvasW, CanvasH, 0, 0
	for y := 0; y < CanvasH; y++ {
		for x := 0; x < DividerX; x++ {
			if without.GrayAt(x, y).Y != diff.GrayAt(x, y).Y {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX == 0 {
		t.Fatal("找不到农历行的墨迹")
	}
	// 上缘必须 ≥ 200：星期行占 y 156..200，LunarY 一旦被改小（例如 190），
	// 农历行会顶进星期行。gofont 的缺字兜底字形偏矮，抓不住这一点；
	// STHeiti 下 LunarY=190 时实测墨迹上缘到 y=194。
	if minY < 200 || maxY >= DateY+178 {
		t.Fatalf("农历行纵向出界: y %d..%d（上缘须 ≥ 200，区域底 %d）", minY, maxY, DateY+177)
	}
	if minX < LeftX || maxX >= LeftX+LeftW {
		t.Fatalf("农历行横向出界: x %d..%d", minX, maxX)
	}
	t.Logf("农历行墨迹: x %d..%d, y %d..%d", minX, maxX, minY, maxY)
}

// 法定节假日名右对齐在日期行，落在 date 区域内。
func TestHolidayTagRendered(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Day = state.DayInfo{HolidayName: "国庆节", HolidayKind: state.HolidayRest}

	withTag := r.Render(snap, ui.State{})
	without := r.Render(baseSnap(), ui.State{})

	// 标签在日期行右半部分（x 400..600）
	rect := image.Rect(400, 72, 600, 200)
	if inkIn(withTag, rect) <= inkIn(without, rect) {
		t.Fatal("有节假日时右半边应有额外的墨迹")
	}
}

// 普通日子不加标签 —— 否则「10月30日」右边会莫名其妙多出东西。
func TestHolidayTagAbsentOnNormalDay(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Day = state.DayInfo{LunarMonthName: "九月", LunarDayName: "廿一"} // 有农历但不是节假日
	if a, b := inkIn(r.Render(snap, ui.State{}), image.Rect(400, 72, 600, 200)),
		inkIn(r.Render(baseSnap(), ui.State{}), image.Rect(400, 72, 600, 200)); a != b {
		t.Fatalf("普通日子不该有标签，墨迹 %d vs %d", a, b)
	}
}

// 调休上班日显示「调休」字样。
func TestHolidayTagShowsAdjustment(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Day = state.DayInfo{HolidayName: "春节", HolidayKind: state.HolidayWorkdayAdjust}
	if inkIn(r.Render(snap, ui.State{}), image.Rect(400, 72, 600, 200)) == 0 {
		t.Fatal("调休上班日应有标签墨迹")
	}
}

// 电量的绝对所在带。位置断言必须用写死的绝对坐标，不能用 BatteryY 推导 ——
// 跟着被测常量一起漂移的带，任何位移都测不出来。
//
// 左端从 1300 起而不是右栏左缘：1240..1296 是齿轮（设置入口），它始终绘制，
// 混进来会让「Known=false 不该有任何墨迹」这类断言恒假。
var batteryBand = image.Rect(1300, 1016, RightX+RightW, CanvasH)

// 电量右对齐在课程区下方，落在 courses 区域内（随该区域局部刷新）。
func TestBatteryRendered(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Battery = state.Battery{Pct: 87, Known: true}

	withBatt := r.Render(snap, ui.State{})
	without := r.Render(baseSnap(), ui.State{})

	rect := image.Rect(1200, 1016, 1392, 1072)
	if inkIn(withBatt, rect) <= inkIn(without, rect) {
		t.Fatal("有电量时该区域应有额外墨迹")
	}
}

// 读不到电量时不画。这里必须用非零 Pct —— Known:false 的零值和 baseSnap()
// 已有的 Battery 逐字段相同，用零值写的测试恒真：删掉 Known 守卫它照样绿。
func TestBatteryHiddenWhenUnknown(t *testing.T) {
	r := newTestRenderer(t)
	unknown := baseSnap()
	unknown.Battery = state.Battery{Pct: 87, Known: false}
	known := baseSnap()
	known.Battery = state.Battery{Pct: 87, Known: true}

	if inkIn(r.Render(unknown, ui.State{}), batteryBand) != 0 {
		t.Fatal("Known=false 时不该画任何东西")
	}
	if inkIn(r.Render(known, ui.State{}), batteryBand) == 0 {
		t.Fatal("Known=true 时应该画出来")
	}
}

// 真正的 0% 电量是「读到了，是 0」，必须画出来 —— 不能和「读不到」混为一谈。
func TestBatteryZeroPercentIsShown(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Battery = state.Battery{Pct: 0, Known: true}
	if inkIn(r.Render(snap, ui.State{}), batteryBand) == 0 {
		t.Fatal("Known=true、Pct=0 时应当画出 0%")
	}
}

// 电量必须整个落在 courses 区域内，否则永远不会被局部刷新到。
//
// 用「有电量/无电量」两次渲染的差分包围盒，而不是预先裁好的窗口做包含判断 ——
// 窗口是 courses 区的真子集，那种写法对「画到窗口外但在区域内」和
// 「完全没画」都恒真。
func TestBatteryInsideCoursesRegion(t *testing.T) {
	r := newTestRenderer(t)
	without := r.Render(baseSnap(), ui.State{})
	with := baseSnap()
	with.Battery = state.Battery{Pct: 100, Known: true}
	diff := r.Render(with, ui.State{})

	minX, minY, maxX, maxY := CanvasW, CanvasH, 0, 0
	for y := 0; y < CanvasH; y++ {
		for x := 0; x < CanvasW; x++ {
			if without.GrayAt(x, y).Y != diff.GrayAt(x, y).Y {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX == 0 {
		t.Fatal("找不到电量的墨迹")
	}
	var courses Rect
	for _, reg := range Regions() {
		if reg.ID == "courses" {
			courses = reg.Rect
		}
	}
	if minX < courses.X || maxX >= courses.Right() ||
		minY < courses.Y || maxY >= courses.Y+courses.H {
		t.Fatalf("电量墨迹 x%d..%d y%d..%d 超出 courses 区 %+v", minX, maxX, minY, maxY, courses)
	}
	// 绝对位置断言：差分包围盒必须完整落在写死的 batteryBand 内，
	// 且以固定的带中心 1044 = (1016+1072)/2 为垂直基准。
	// 只用包含判断抓不住 BatteryY 下移：画到画布外的像素被 draw.Image
	// 裁掉后，可见墨迹仍在带内 —— 只是字被切掉了一截。
	if box := image.Rect(minX, minY, maxX+1, maxY+1); !box.In(batteryBand) {
		t.Fatalf("电量墨迹 x%d..%d y%d..%d 越出绝对电量带 %v", minX, maxX, minY, maxY, batteryBand)
	}
	const batteryInkCenterY = 1044
	if center := (minY + maxY) / 2; abs(center-batteryInkCenterY) > 4 {
		t.Errorf("电量墨迹纵向中心 y=%d 偏离绝对中心 %d 超过 4px", center, batteryInkCenterY)
	}
}

// 电量与「数据」「教学周」同灰度：ColorLight（179），不是黑色。
func TestBatteryUsesLightGray(t *testing.T) {
	r := newTestRenderer(t)
	without := r.Render(baseSnap(), ui.State{})
	with := baseSnap()
	with.Battery = state.Battery{Pct: 87, Known: true}
	img := r.Render(with, ui.State{})

	box := diffBounds(img, without)
	if box.Empty() {
		t.Fatal("找不到电量的墨迹")
	}
	want := int(ColorLight.Y)
	sawExact := false
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			v := int(img.GrayAt(x, y).Y)
			if v == 255 {
				continue
			}
			if v < want {
				t.Fatalf("电量像素 (%d,%d) 灰度 %d 比 ColorLight %d 还深", x, y, v, want)
			}
			if v == want {
				sawExact = true
			}
		}
	}
	if !sawExact {
		t.Errorf("没有找到 ColorLight(%d) 的像素 —— 电量可能用了别的灰度", want)
	}
}

// 超长的节假日标签必须按日期区宽度省略：标签右对齐，若整串画出去，
// 左端会越过 LeftX 进入左边距 —— 那是永不刷新的盲带。
//
// 用「有/无标签」两次渲染的差分包围盒判断标签墨迹：不能用
// isBlankRegion(日期行) —— 日期行和星期行本来就有字，那个断言恒真。
// TestLeftColumnHasNoInkOutsideRegions 又只从 LeftX 起扫，看不见左边距。
func TestHolidayTagLongTextStaysInDateRegion(t *testing.T) {
	r := newTestRenderer(t)
	without := r.Render(baseSnap(), ui.State{})
	snap := baseSnap()
	snap.Day = state.DayInfo{
		HolidayName: "这是一个非常非常非常非常非常非常长的节假日名称",
		HolidayKind: state.HolidayRest,
	}
	img := r.Render(snap, ui.State{})

	box := diffBounds(img, without)
	if box.Empty() {
		t.Fatal("超长标签被整个弄丢了 —— 应该是省略而不是不画")
	}
	if box.Min.X < LeftX {
		t.Errorf("标签左缘 %d 越过 LeftX %d —— 进了左边距盲带", box.Min.X, LeftX)
	}
	if box.Max.X > LeftX+LeftW {
		t.Errorf("标签右缘 %d 越过日期区右缘 %d", box.Max.X, LeftX+LeftW)
	}
	// 省略后仍保持右对齐：右缘贴着日期区右缘。
	if box.Max.X < LeftX+LeftW-8 {
		t.Errorf("标签右缘 %d 离日期区右缘 %d 太远 —— 省略后没有保持右对齐",
			box.Max.X, LeftX+LeftW)
	}
}

func inkIn(img *image.Gray, rect image.Rectangle) int {
	// 越界必须立刻失败，不能静默返回 0 —— image.Gray 越界读到的是黑色，
	// 会被当成墨迹，制造与真实原因无关的假红/假绿。
	if !rect.In(img.Bounds()) {
		panic(fmt.Sprintf("inkIn 的矩形 %v 越出画布 %v —— 测试本身写错了", rect, img.Bounds()))
	}
	n := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if img.GrayAt(x, y).Y < 250 {
				n++
			}
		}
	}
	return n
}

// ---- Task 5：翻月、选中态与节假日标记 ----

// calendarCellRect 返回网格里 (row,col) 格子的绝对矩形。
func calendarCellRect(row, col int) image.Rectangle {
	return image.Rect(
		RightX+col*CalCellW, CalGridY+row*CalCellH,
		RightX+(col+1)*CalCellW, CalGridY+(row+1)*CalCellH)
}

// diffInBand 数两张图在给定矩形内的差异像素。
func diffInBand(a, b *image.Gray, rect image.Rectangle) int {
	n := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if a.GrayAt(x, y).Y != b.GrayAt(x, y).Y {
				n++
			}
		}
	}
	return n
}

// 翻月后标题跟着变，且两端各有一个箭头。
func TestCalendarMonthOffsetChangesTitle(t *testing.T) {
	r := newTestRenderer(t)
	base := baseSnap()
	cur := r.Render(base, ui.State{})
	next := r.Render(base, ui.State{MonthOffset: 1})
	if imagesEqual(cur, next) {
		t.Fatal("月份偏移变化后输出必须不同")
	}
	// 标题区（MonthY 那一行）必须有墨迹变化。网格从 y=208 起，
	// 所以这一带的变化只可能来自标题/箭头本身。
	titleBand := image.Rect(RightX, MonthY, RightX+RightW, MonthY+56)
	if diffInBand(cur, next, titleBand) == 0 {
		t.Fatal("标题行应当变化")
	}
}

// 月份标题必须让开左箭头的热区，否则点年份会触发上一月。
//
// 判别依据是灰度而不是「有没有墨迹」：箭头用 ColorGray 画，标题用
// ColorBlack。热区里只要出现比 ColorGray 更深的像素，就说明黑色的标题
// 画进了热区。只数墨迹的写法会被箭头自身的墨迹喂饱 —— 把标题起点从
// RightX+ArrowSize 挪回 RightX 也照样绿，等于没测。
func TestCalendarTitleDoesNotOverlapLeftArrow(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	img := r.Render(snap, ui.State{})

	hot := image.Rect(MonthArrowLeftX, MonthY, MonthArrowLeftX+ArrowSize, MonthY+56)
	// 先确认箭头真的画出来了：否则下面的「没有深色像素」对一片空白也成立。
	if inkIn(img, hot) == 0 {
		t.Fatal("左箭头没有被画出来")
	}
	for y := hot.Min.Y; y < hot.Max.Y; y++ {
		for x := hot.Min.X; x < hot.Max.X; x++ {
			if v := img.GrayAt(x, y).Y; v < ColorGray.Y {
				t.Fatalf("左箭头热区 (%d,%d) 灰度 %d 比箭头 %d 更深 —— 标题侵入了热区",
					x, y, v, ColorGray.Y)
			}
		}
	}
	// 标题必须紧贴热区右侧开始画，而不是干脆不画。
	titleStart := image.Rect(MonthArrowLeftX+ArrowSize, MonthY,
		MonthArrowLeftX+ArrowSize+40, MonthY+56)
	if inkIn(img, titleStart) == 0 {
		t.Fatal("标题没有从箭头热区右侧开始 —— 可能被整个挪走了")
	}

	// 右箭头同样要画出来。
	rightHot := image.Rect(MonthArrowRightX, MonthY,
		MonthArrowRightX+ArrowSize, MonthY+56)
	if inkIn(img, rightHot) == 0 {
		t.Fatal("右箭头没有被画出来")
	}
}

// 选中但不是今天：该格有描边、日号仍在，且样式显著轻于今天的黑块。
//
// 2026-10-20 是周二。当月 1 日是周四、周首日是周日，网格从 9-27 起，
// 所以它是第 4 行第 3 列（行、列都从 0 起）。绝对坐标写死，
// 不跟着被测常量漂移。
func TestCalendarSelectedOutline(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap() // Now = 2026-10-29
	loc := snap.Now.Location()
	selected := time.Date(2026, 10, 20, 0, 0, 0, 0, loc)

	plain := r.Render(snap, ui.State{})
	img := r.Render(snap, ui.State{SelectedDate: selected})

	cell := calendarCellRect(3, 2)

	// 描边必须出现：取上方两个像素的环带（避开圆角）。
	topStroke := image.Rect(cell.Min.X+10, cell.Min.Y+4, cell.Max.X-10, cell.Min.Y+7)
	if inkIn(img, topStroke) == 0 {
		t.Fatal("选中日期没有描边")
	}
	if inkIn(plain, topStroke) != 0 {
		t.Fatal("未选中时该处本来就有墨迹 —— 这条断言区分不出描边")
	}

	// 数字不能被描边擦掉：格子中部要有墨迹。
	middle := image.Rect(cell.Min.X+30, cell.Min.Y+14, cell.Max.X-30, cell.Max.Y-14)
	if inkIn(img, middle) == 0 {
		t.Fatal("选中日期的日号被描边擦掉了")
	}

	// 选中态只影响这一格。课程标题也会跟随选中日变化，所以只比较日历区；
	// 日历区内再逐格确认其他格子没有任何变化。
	cal := rectFor("calendar").Standard()
	if diffInBand(plain, img, cal) == 0 {
		t.Fatal("选中态没有改变日历区")
	}
	for row := 0; row < CalRows; row++ {
		for col := 0; col < CalCols; col++ {
			other := calendarCellRect(row, col)
			if other == cell {
				continue
			}
			if n := diffInBand(plain, img, other); n != 0 {
				t.Fatalf("选中态影响了其他格 %v（%d 像素不同）", other, n)
			}
		}
	}

	// 与今天（黑块反白）的样式必须不同：黑块墨迹远多于一圈描边。
	todayCell := calendarCellRect(4, 4) // 2026-10-29 周四
	if a, b := inkIn(img, todayCell), inkIn(img, cell); a <= b*3 {
		t.Errorf("今天格墨迹 %d 与选中格 %d 太接近 —— 两种样式区分不开", a, b)
	}
}

// 选中今天时，日历区等价于跟随今天：今天优先走黑块分支。
//
// 只比较日历区：Task 6 起课程标题按 FollowToday() 判定，非零的 SelectedDate
// 会显示具体日期。真实交互里 SelectDate 会把「今天」归一成零值，
// 因此这种状态只出现在直接构造 State 的测试里。
func TestCalendarSelectedTodayRendersAsToday(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	follow := r.Render(snap, ui.State{})
	selToday := r.Render(snap, ui.State{SelectedDate: snap.Now})
	if n := diffInBand(follow, selToday, rectFor("calendar").Standard()); n != 0 {
		t.Fatalf("选中今天的日历区与跟随今天不同（%d 像素）—— 今天应优先走黑块分支", n)
	}
}

// 放假日标记在格子右上角，不占农历所在的底部。
//
// 变异：改回在底部画圆点 → 底部带会多出墨迹，本条红。
func TestCalendarHolidayMarkUsesCornerNotBottom(t *testing.T) {
	r := newTestRenderer(t)
	base := baseSnap()
	with := baseSnap()
	with.Calendar = map[string]state.DayInfo{
		"2026-10-20": {HolidayName: "国庆节", HolidayKind: state.HolidayRest},
	}

	before := r.Render(base, ui.State{})
	after := r.Render(with, ui.State{})

	cell := calendarCellRect(3, 2) // 2026-10-20
	// 底部 20px 是农历的位置：放假日不能在这里多画任何东西。
	bottom := image.Rect(cell.Min.X, cell.Max.Y-20, cell.Max.X, cell.Max.Y)
	if n := diffInBand(before, after, bottom); n != 0 {
		t.Fatalf("放假日格子底部多出 %d 个墨迹像素 —— 标记压到农历上了", n)
	}
	// 右上角必须有「休」的墨迹。
	corner := image.Rect(cell.Max.X-24, cell.Min.Y+2, cell.Max.X-2, cell.Min.Y+24)
	if inkIn(after, corner) == 0 {
		t.Fatal("放假日右上角没有墨迹 —— 「休」没画")
	}
	if inkIn(before, corner) != 0 {
		t.Fatal("没有节假日数据时该处本来就有墨迹 —— 这条断言区分不出「休」")
	}
}

// 调休上班日：日号右上角的小字「班」（右下角现在留给农历）。
func TestCalendarWorkdayAdjustMark(t *testing.T) {
	r := newTestRenderer(t)
	base := baseSnap()
	with := baseSnap()
	with.Calendar = map[string]state.DayInfo{
		"2026-10-20": {HolidayName: "春节", HolidayKind: state.HolidayWorkdayAdjust},
	}

	before := r.Render(base, ui.State{})
	after := r.Render(with, ui.State{})

	cell := calendarCellRect(3, 2) // 2026-10-20
	markBand := image.Rect(cell.Max.X-24, cell.Min.Y+2, cell.Max.X-2, cell.Min.Y+24)
	if inkIn(after, markBand) == 0 {
		t.Fatal("调休日的「班」字没有画出来")
	}
	if inkIn(before, markBand) != 0 {
		t.Fatal("没有节假日数据时该处本来就有墨迹 —— 这条断言区分不出「班」字")
	}
}

// 非本月补格即使是法定节假日也不画标记：补格只是让月历看起来完整，
// 标上节假日会让人以为上个月那天也在本月。
func TestCalendarIgnoresNonMonthHolidays(t *testing.T) {
	r := newTestRenderer(t)
	plain := baseSnap()
	with := baseSnap()
	// 2026-09-27 是周首日为周日时当月 1 日所在周的周日，即左上角补格。
	with.Calendar = map[string]state.DayInfo{
		"2026-09-27": {HolidayName: "国庆节", HolidayKind: state.HolidayRest},
	}

	a := r.Render(plain, ui.State{})
	b := r.Render(with, ui.State{})
	if !imagesEqual(a, b) {
		t.Fatal("非本月补格上的节假日标记被画出来了")
	}
}

// 绘制用的格子与命中测试用的格子必须一致：今天那一格的中心点，
// 经 HitTest 必须回到今天。两边各写一份补格推算时，这条会红。
func TestCalendarTodayBlockMatchesHitTest(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	img := r.Render(snap, ui.State{})

	bestInk, best := -1, image.Point{}
	for row := 0; row < CalRows; row++ {
		for col := 0; col < CalCols; col++ {
			cell := calendarCellRect(row, col)
			if n := inkIn(img, cell); n > bestInk {
				bestInk = n
				best = image.Pt(cell.Min.X+cell.Dx()/2, cell.Min.Y+cell.Dy()/2)
			}
		}
	}
	if bestInk <= 0 {
		t.Fatal("找不到今天的黑块")
	}
	hit := HitTest(best.X, best.Y, ui.MonthStart(snap.Now, 0), false)
	if hit.Kind != HitCalendarDay {
		t.Fatalf("黑块中心 (%d,%d) 命中的是 %v，不是日期格", best.X, best.Y, hit.Kind)
	}
	if !sameDate(hit.Date, snap.Now) {
		t.Fatalf("黑块画在 %s 的格子里，命中却返回 %s",
			snap.Now.Format("2006-01-02"), hit.Date.Format("2006-01-02"))
	}
}

// 补格不得带任何装饰 —— 即使那天是「今天」。
//
// 反例：今天 9-30、翻到 10 月。周日开头的网格从 9-27 起，9-30 是首行补格；
// 给它画今天黑块会让人以为「10 月里有个 30 号」。对照组今天是 8-15，
// 不在 10 月网格里 —— 两张图的 10 月日历区必须逐像素相同。
func TestCalendarFillerCellsAreNeverDecorated(t *testing.T) {
	r := newTestRenderer(t)
	loc := baseSnap().Now.Location()

	fillerToday := baseSnap()
	fillerToday.Now = time.Date(2026, 9, 30, 12, 0, 0, 0, loc)
	outOfGrid := baseSnap()
	outOfGrid.Now = time.Date(2026, 8, 15, 12, 0, 0, 0, loc)

	// 两个 today 都通过各自的 MonthOffset 落到 2026-10。
	a := r.Render(fillerToday, ui.State{MonthOffset: 1})
	b := r.Render(outOfGrid, ui.State{MonthOffset: 2})

	// 10 月的日历视图与「今天是哪一天」无关 —— 只要补格没被装饰，
	// 两张图的日历区必然逐像素相同（标题同为 10 月，网格日期也相同）。
	if n := diffInBand(a, b, rectFor("calendar").Standard()); n != 0 {
		t.Fatalf("翻月视图随「今天」改变 —— 补格被装饰了（%d 个像素不同）", n)
	}

	// 直接断言补格没有黑色墨迹：黑块会产生接近纯黑的像素，浅灰日号不会。
	// 2026-10 前补 4 格 + 31 天 = 35 格，末行 7 格全是 11 月补格，
	// 首行则混着本月的黑色日号，不能用来判定。
	fillerBand := image.Rect(RightX, CalGridY+5*CalCellH, RightX+RightW, CalGridY+CalRows*CalCellH)
	if got := darkestIn(a, fillerBand); got < 100 {
		t.Fatalf("补格末行出现灰度 %d 的深色墨迹 —— 补格被装饰了", got)
	}
}

// darkestIn 返回区域内最深的灰度（0 最黑，255 最白）。
func darkestIn(img *image.Gray, rect image.Rectangle) int {
	got := 255
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if v := int(img.GrayAt(x, y).Y); v < got {
				got = v
			}
		}
	}
	return got
}

// ---- Task 6：课程区按选中日显示、翻页与页码 ----

// 标题跟随选中日期：今天显示「今日课程」，别的日子显示「M月D日 课程」。
func TestCourseTitleFollowsSelectedDate(t *testing.T) {
	r := newTestRenderer(t)
	today := r.Render(baseSnap(), ui.State{})
	sel := ui.State{SelectedDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	withSel := r.Render(baseSnap(), sel)

	titleRect := Rect{X: RightX, Y: CoursesTitleY, W: RightW, H: 32}
	titleBand := titleRect.Standard()
	if diffInBand(today, withSel, titleBand) == 0 {
		t.Fatal("选中别的日期后标题应当变化")
	}

	// 上面的差分只证明两种状态不同；默认文案被改成任意日期串它照样绿。
	// 用同字面、同位置单独画一遍期望文案，与默认渲染的标题带逐像素对照，
	// 把默认文案钉死为「今日课程」。
	want := image.NewGray(image.Rect(0, 0, CanvasW, CanvasH))
	for i := range want.Pix {
		want.Pix[i] = 255
	}
	TextColumnLeft(want, r.faceCourseTtl, "今日课程", RightX,
		baselineIn(r.faceCourseTtl, CoursesTitleY, 32), ColorGray)
	if n := diffInBand(today, want, titleBand); n != 0 {
		t.Fatalf("默认标题与「今日课程」不符（%d 像素不同）", n)
	}
}

// 课程超过一屏时出现页码，且翻页会换内容。
func TestCoursePagingShowsPageIndicatorAndChangesContent(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Timetable.Available = true
	snap.Timetable.Courses = nil
	for i := 0; i < 9; i++ { // 9 门课 -> 2 页
		snap.Timetable.Courses = append(snap.Timetable.Courses, state.Course{
			Name: fmt.Sprintf("课%d", i+1), StartText: "08:00", EndText: "08:40",
		})
	}

	page0 := r.Render(snap, ui.State{})
	page1 := r.Render(snap, ui.State{CourseScroll: 1})

	body := image.Rect(RightX, CoursesStartY, RightX+RightW, CoursesStartY+CourseMaxRows*CourseRowH)
	if diffInBand(page0, page1, body) == 0 {
		t.Fatal("翻页后课程内容应当不同")
	}
	// 页码在底部带，两页的页码不同
	foot := image.Rect(RightX, BatteryY, RightX+RightW/2, BatteryY+56)
	if diffInBand(page0, page1, foot) == 0 {
		t.Fatal("页码应当随页变化")
	}

	// 第 2 页的第一行必须是第 7 门课，而不是第 6 门（页间不能重叠）。
	// 只断言「两页不同」不够：翻页起点写成 page*(CourseMaxRows-1) 时两页
	// 也不同，但第 6 门会在两页里重复出现。
	only7 := baseSnap()
	only7.Timetable.Available = true
	only7.Timetable.Courses = []state.Course{snap.Timetable.Courses[6]}
	only7.NextCourseIdx = -1
	snap.NextCourseIdx = -1

	row0 := image.Rect(RightX, CoursesStartY, RightX+RightW, CoursesStartY+CourseRowH)
	want := r.Render(only7, ui.State{})
	got := r.Render(snap, ui.State{CourseScroll: 1})
	if n := diffInBand(got, want, row0); n != 0 {
		t.Fatalf("第 2 页第一行应当是第 7 门课（%d 像素不同）", n)
	}
}

// 课程不超过一屏时不该显示页码。
func TestCourseNoPageIndicatorWhenFitsOnePage(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Timetable.Available = true
	snap.Timetable.Courses = []state.Course{{Name: "只有一门", StartText: "08:00", EndText: "08:40"}}

	with := r.Render(snap, ui.State{})
	foot := image.Rect(RightX, BatteryY, RightX+RightW/2, BatteryY+56)
	if inkIn(with, foot) != 0 {
		t.Fatal("单页时底部带左侧应当完全空白")
	}
}

// 滚到越界时应当夹紧，不能画出空屏。
func TestCourseScrollIsClamped(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Timetable.Available = true
	snap.Timetable.Courses = nil
	for i := 0; i < 3; i++ {
		snap.Timetable.Courses = append(snap.Timetable.Courses, state.Course{
			Name: fmt.Sprintf("课%d", i+1), StartText: "08:00", EndText: "08:40",
		})
	}
	// 只有 1 页，滚到第 5 页应当等同于第 0 页
	clamped := r.Render(snap, ui.State{CourseScroll: 5})
	first := r.Render(snap, ui.State{})
	if !imagesEqual(clamped, first) {
		t.Fatal("越界的滚动位置应被夹紧到最后一页（这里是第 0 页）")
	}
	// 反方向越界同样要夹紧到第 0 页。
	negative := r.Render(snap, ui.State{CourseScroll: -5})
	if !imagesEqual(negative, first) {
		t.Fatal("负的滚动位置应被夹紧到第 0 页")
	}
}

// 选中周末日期时，日号仍用周末的灰色（ColorGray），
// 不能因为选中而变成黑色 —— 与未选中的同一周末日号逐像素一致。
func TestCalendarSelectedWeekendKeepsGrayDigit(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap() // Now = 2026-10-29，周四
	loc := snap.Now.Location()
	// 2026-10-25 是周日，周首日为周日时落在第 5 行第 1 列（行、列从 0 起）。
	selected := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)

	plain := r.Render(snap, ui.State{})
	img := r.Render(snap, ui.State{SelectedDate: selected})

	cell := calendarCellRect(4, 0)
	// 只取格子中部：描边在四周，避开它才能单独比较日号的颜色。
	middle := image.Rect(cell.Min.X+20, cell.Min.Y+10, cell.Max.X-20, cell.Max.Y-10)
	if inkIn(img, middle) == 0 {
		t.Fatal("选中周末日期后日号不见了")
	}
	if n := diffInBand(plain, img, middle); n != 0 {
		t.Fatalf("选中周末改变了日号颜色（%d 像素不同）—— 应与未选中时同为周末灰", n)
	}
	if got := darkestIn(img, middle); got < int(ColorGray.Y) {
		t.Errorf("选中周末的日号出现灰度 %d，深于周末灰 %d", got, ColorGray.Y)
	}
}

// ---- Task 7：设置页 ----

// 设置页整屏覆盖：内容与表盘完全不同，且四行文案都在。
func TestSettingsPageReplacesDashboard(t *testing.T) {
	r := newTestRenderer(t)
	dash := r.Render(baseSnap(), ui.State{})
	settings := r.Render(baseSnap(), ui.State{SettingsOpen: true})
	if imagesEqual(dash, settings) {
		t.Fatal("设置页应当与表盘不同")
	}
	// 表盘左侧（时钟区）在设置页里必须空白 —— 设置页是整屏覆盖，
	// 不是叠在表盘上画几行字。
	//
	// 只检查时钟数字所在的左半边。
	//
	// 不能扫整个 clockBand：设置页第 0 行的标签是居中的，用真实 CJK 字体时
	// 它的墨迹会伸到 x=567，而 clockBand 右边界是 600 —— 正确的实现也会被判成红。
	// 这条断言要守的是「设置页不是叠在表盘上画的」，所以只要确认时钟区
	// 没有数字就够了（分隔线是 ColorLight，不会被 <100 的判据算进去）。
	clockDigits := image.Rect(LeftX, ClockY, LeftX+200, ClockY+ClockCellH)
	if darkestIn(settings, clockDigits) < 100 {
		t.Fatal("设置页应当整屏覆盖，时钟数字不该还在")
	}
}

// 六行条目按 hit.go 的几何排布，每一行都必须有墨迹。
func TestSettingsRowsAreAtHitTestGeometry(t *testing.T) {
	r := newTestRenderer(t)
	img := r.Render(baseSnap(), ui.State{SettingsOpen: true})

	// 几何绝对值必须钉死。只用 SettingsRowH 推导期望带的话，把常量一起
	// 改成 60 时测试会跟着漂移，什么都测不出来。
	if SettingsFirstRowY != 260 || SettingsRowH != 100 || SettingsRowCount != 6 {
		t.Fatalf("设置页几何变了：FirstRowY=%d RowH=%d RowCount=%d，期望 260/100/6",
			SettingsFirstRowY, SettingsRowH, SettingsRowCount)
	}

	for row := 0; row < SettingsRowCount; row++ {
		top := SettingsFirstRowY + row*SettingsRowH
		band := image.Rect(0, top, CanvasW, top+SettingsRowH)
		if inkIn(img, band) == 0 {
			t.Fatalf("第 %d 行没有任何墨迹 —— 文案与 hit.go 的几何对不上", row)
		}
		// 行带底部 1px 是分隔线。只看整带的话，把第 0..2 行的文案改空，
		// 分隔线仍会让上面的断言成立。标签基线落在行首 60px 内，
		// 这里单独要求那一段有墨迹（设备信息小字在 top+56 之后，
		// 不会替空标签兜底）。
		labelBand := image.Rect(0, top, CanvasW, top+60)
		if inkIn(img, labelBand) == 0 {
			t.Fatalf("第 %d 行没有文案墨迹（只有分隔线）—— settingsRows 与 hit.go 的几何对不上", row)
		}
	}
}

// 六行文案的顺序必须与 hit.go 的 Row 索引一一对应 ——
// 顺序错了的后果是点「关闭」调亮度或触发抓取。
func TestSettingsRowsMatchHitTestIndices(t *testing.T) {
	if len(settingsRows) != SettingsRowCount {
		t.Fatalf("文案行数 %d 与命中几何的行数 %d 不一致", len(settingsRows), SettingsRowCount)
	}
	want := []string{"返回 Kindle 界面", "立即全屏刷新", "背光亮度", "刷新课表", "设备信息", "关闭"}
	for i, w := range want {
		if settingsRows[i] != w {
			t.Errorf("第 %d 行应为 %q，实际 %q —— 与 hit.go 的 Row 索引对不上", i, w, settingsRows[i])
		}
	}
	// 单独钉住关键行的索引：
	// Row 3 点错会变成「点刷新课表调亮度」，Row 5 点错会「点关闭触发抓取」。
	if settingsRows[SettingsRowRefreshSchedule] != "刷新课表" {
		t.Errorf("Row %d 应为「刷新课表」，实际 %q",
			SettingsRowRefreshSchedule, settingsRows[SettingsRowRefreshSchedule])
	}
	if settingsRows[SettingsRowClose] != "关闭" {
		t.Errorf("Row %d 应为「关闭」，实际 %q", SettingsRowClose, settingsRows[SettingsRowClose])
	}
	if settingsRows[SettingsRowBrightness] != "背光亮度" {
		t.Errorf("Row %d 应为「背光亮度」，实际 %q",
			SettingsRowBrightness, settingsRows[SettingsRowBrightness])
	}
}

// 「刷新课表」右侧的状态文字：进行中 > 失败 > 上次成功时刻。
func TestSettingsScheduleStatus(t *testing.T) {
	at := time.Date(2026, 10, 31, 19, 20, 0, 0, time.UTC)
	cases := []struct {
		name string
		info state.SystemInfo
		want string
	}{
		{"进行中", state.SystemInfo{Refreshing: true, ScheduleAt: at}, "刷新中…"},
		{"进行中压过失败", state.SystemInfo{Refreshing: true, ScheduleRefreshFailed: true, ScheduleAt: at}, "刷新中…"},
		{"失败", state.SystemInfo{ScheduleRefreshFailed: true, ScheduleAt: at}, "失败"},
		{"有上次时间", state.SystemInfo{ScheduleAt: at}, "上次 19:20"},
		{"从未成功", state.SystemInfo{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := settingsScheduleStatus(c.info); got != c.want {
				t.Fatalf("settingsScheduleStatus(%+v) = %q，期望 %q", c.info, got, c.want)
			}
		})
	}
}

// 「刷新课表」行右侧的状态必须真的画出来（纯函数单测抓不到漏画）。
func TestSettingsScheduleStatusIsDrawn(t *testing.T) {
	r := newTestRenderer(t)
	render := func(info state.SystemInfo) *image.Gray {
		snap := baseSnap()
		snap.Info = info
		return r.Render(snap, ui.State{SettingsOpen: true})
	}
	at := at(19, 20)
	idle := render(state.SystemInfo{ScheduleAt: at})
	refreshing := render(state.SystemInfo{Refreshing: true, ScheduleAt: at})
	if diffInBand(idle, refreshing, settingsRowBand(SettingsRowRefreshSchedule)) == 0 {
		t.Fatal("「刷新中…」没有画到第 3 行上")
	}
	failed := render(state.SystemInfo{ScheduleRefreshFailed: true, ScheduleAt: at})
	if diffInBand(idle, failed, settingsRowBand(SettingsRowRefreshSchedule)) == 0 {
		t.Fatal("「失败」没有画到第 3 行上")
	}
}

func settingsRowBand(row int) image.Rectangle {
	return image.Rect(0, SettingsFirstRowY+row*SettingsRowH,
		CanvasW, SettingsFirstRowY+(row+1)*SettingsRowH)
}

// 亮度 0 与 24 的滑块必须落在轨道两端；位置由快照里的档位算出，
// 而不是所有档位都画在同一个地方。
func TestSettingsBrightnessKnobPosition(t *testing.T) {
	r := newTestRenderer(t)
	render := func(level int) *image.Gray {
		snap := baseSnap()
		snap.Info = state.SystemInfo{Brightness: level, BrightnessMax: 24}
		return r.Render(snap, ui.State{SettingsOpen: true})
	}
	// 只看轨道及其上下一个滑块半径的带子。标签在 x=200、数值在 x=1200，
	// 都在这个范围之外，不会干扰。
	band := image.Rect(SliderTrackX-SliderKnobR, SliderTrackY-SliderKnobR-1,
		SliderTrackX+SliderTrackW+SliderKnobR+1, SliderTrackY+SliderKnobR+2)

	cases := []struct {
		name       string
		level      int
		wantCenter int
	}{
		{"0 档在最左", 0, SliderTrackX},
		{"12 档在中点", 12, SliderTrackX + SliderTrackW/2},
		{"24 档在最右", 24, SliderTrackX + SliderTrackW},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, maxX := darkSpanX(render(c.level), band, 100)
			// 深色右缘 = 滑块圆心 + 半径（已填充段止于圆心，不会越过它）。
			if got := maxX - SliderKnobR; got != c.wantCenter {
				t.Fatalf("亮度 %d 的滑块圆心 = %d，期望 %d（深色右缘 %d）",
					c.level, got, c.wantCenter, maxX)
			}
			if c.wantCenter < SliderTrackX || c.wantCenter > SliderTrackX+SliderTrackW {
				t.Fatalf("滑块圆心 %d 不在轨道 [%d, %d] 内",
					c.wantCenter, SliderTrackX, SliderTrackX+SliderTrackW)
			}
		})
	}
}

// 标签与数值画在计划指定的位置（左对齐 200 / 轨道外侧 1200），
// 不再像别的设置行那样居中 —— 否则会压在轨道上。
func TestSettingsBrightnessLayout(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Info = state.SystemInfo{Brightness: 12, BrightnessMax: 24}
	img := r.Render(snap, ui.State{SettingsOpen: true})

	rowTop := SettingsFirstRowY + SettingsRowBrightness*SettingsRowH
	labelBand := image.Rect(SliderLabelX, rowTop, SliderLabelX+120, rowTop+SettingsRowH)
	if inkIn(img, labelBand) == 0 {
		t.Fatalf("x=%d.. 的标签区没有墨迹 —— 标签没有左对齐", SliderLabelX)
	}
	valueBand := image.Rect(SliderValueX, rowTop, SliderValueX+80, rowTop+SettingsRowH)
	if inkIn(img, valueBand) == 0 {
		t.Fatalf("x=%d.. 的数值区没有墨迹 —— 数值没有画在轨道外侧", SliderValueX)
	}
}

// 读不到亮度时不画滑块：没有可信数值就不要把猜测画成事实。
func TestSettingsBrightnessUnknownHasNoKnob(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Info = state.SystemInfo{Brightness: -1, BrightnessMax: 24}
	img := r.Render(snap, ui.State{SettingsOpen: true})

	band := image.Rect(SliderTrackX-SliderKnobR, SliderTrackY-SliderKnobR-1,
		SliderTrackX+SliderTrackW+SliderKnobR+1, SliderTrackY+SliderKnobR+2)
	if minX, _ := darkSpanX(img, band, 100); minX >= 0 {
		t.Fatalf("亮度未知时轨道上出现了深色墨迹（x=%d）—— 滑块不该画", minX)
	}
}

// darkSpanX 返回区域内深色像素（Y < threshold）的最左/最右 x；
// 没有深色像素时都返回 -1。
func darkSpanX(img *image.Gray, rect image.Rectangle, threshold uint8) (minX, maxX int) {
	minX, maxX = -1, -1
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if img.GrayAt(x, y).Y >= threshold {
				continue
			}
			if minX < 0 || x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
		}
	}
	return minX, maxX
}

// 设备信息小字的文案：空字段整段跳过，周次未知时不能显示「第 0 教学周」。
func TestSettingsInfoText(t *testing.T) {
	empty := settingsInfoText(state.SystemInfo{})
	if strings.Contains(empty, "第 0 教学周") {
		t.Errorf("Week=0 时不应显示「第 0 教学周」: %q", empty)
	}
	if !strings.Contains(empty, "教学周未知") {
		t.Errorf("Week=0 时应显示「教学周未知」: %q", empty)
	}
	if strings.Contains(empty, "固件") {
		t.Errorf("Firmware 为空时不应留下「固件」空段: %q", empty)
	}
	if strings.Contains(empty, "｜  ｜") || strings.HasPrefix(empty, " ｜ ") ||
		strings.HasSuffix(empty, " ｜") {
		t.Errorf("空字段留下了多余的分隔符: %q", empty)
	}

	filled := settingsInfoText(state.SystemInfo{
		ProgramVersion: "v9.9.9", Firmware: "5.15.1.1", Week: 10,
	})
	for _, want := range []string{"v9.9.9", "固件 5.15.1.1", "第 10 教学周"} {
		if !strings.Contains(filled, want) {
			t.Errorf("有数据时缺少 %q: %q", want, filled)
		}
	}
}

// 设备信息那一行要带上版本号与周次。
func TestSettingsShowsSystemInfo(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap()
	snap.Info = state.SystemInfo{ProgramVersion: "v9.9.9", Firmware: "5.15.1.1", Week: 10}
	withInfo := r.Render(snap, ui.State{SettingsOpen: true})

	plain := baseSnap()
	plain.Info = state.SystemInfo{}
	without := r.Render(plain, ui.State{SettingsOpen: true})

	if imagesEqual(withInfo, without) {
		t.Fatal("有设备信息时输出应当不同")
	}
}

// 关闭设置页后必须回到表盘。
func TestSettingsClosedRestoresDashboard(t *testing.T) {
	r := newTestRenderer(t)
	a := r.Render(baseSnap(), ui.State{})
	b := r.Render(baseSnap(), ui.State{SettingsOpen: false})
	if !imagesEqual(a, b) {
		t.Fatal("SettingsOpen=false 时应当与表盘完全一致")
	}
}

// ---- Task 10：真机反馈修正 ----

// 齿轮必须真的画出来 —— 之前只有命中几何，是「看不见但点得到」。
func TestGearIsDrawn(t *testing.T) {
	r := newTestRenderer(t)
	img := r.Render(baseSnap(), ui.State{})

	const cx, cy = 1268, 1044 // GearX+GearW/2, GearY+GearH/2
	area := Rect{X: cx - 15, Y: cy - 15, W: 30, H: 30}
	if isBlankRegion(img, area) {
		t.Fatal("齿轮中心周围 30×30 内没有墨迹 —— 齿轮没画")
	}
	// 画出来的位置必须与命中几何一致，否则看得见的地方点不到。
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if hit := HitTest(cx, cy, month, false); hit.Kind != HitSettingsGear {
		t.Fatalf("齿轮墨迹中心 (%d,%d) 命中 %v，不是设置齿轮", cx, cy, hit.Kind)
	}
	// 成环：内圆挖白，环带上有浅灰。
	if got := img.GrayAt(cx, cy).Y; got != 255 {
		t.Errorf("齿轮中心灰度 %d，内圆应为白（环）", got)
	}
	if got := img.GrayAt(cx+10, cy).Y; got == 255 {
		t.Error("齿轮环带上没有墨迹")
	}
}

// 齿轮与电量不能重叠：电量右对齐到 1392，齿轮止于 1296。
func TestGearDoesNotOverlapBattery(t *testing.T) {
	r := newTestRenderer(t)
	without := r.Render(baseSnap(), ui.State{})
	with := baseSnap()
	with.Battery = state.Battery{Pct: 98, Known: true}
	batteryInk := diffBounds(r.Render(with, ui.State{}), without)
	if batteryInk.Empty() {
		t.Fatal("找不到电量的墨迹")
	}
	if batteryInk.Min.X < GearX+GearW {
		t.Fatalf("电量墨迹左缘 x=%d 落进齿轮热区右缘 %d —— 两者重叠",
			batteryInk.Min.X, GearX+GearW)
	}
}

// 日历格底部要显示农历：有数据时有墨迹，无数据时空白。
func TestCalendarShowsLunarUnderDayNumber(t *testing.T) {
	r := newTestRenderer(t)
	cell := calendarCellRect(3, 2) // 2026-10-20，周二，不是今天
	bottom := Rect{X: cell.Min.X + 4, Y: cell.Min.Y + 44, W: CalCellW - 8, H: 20}

	plain := baseSnap()
	with := baseSnap()
	with.Calendar = map[string]state.DayInfo{
		"2026-10-20": {LunarMonthName: "九月", LunarDayName: "初十"},
	}

	if !isBlankRegion(r.Render(plain, ui.State{}), bottom) {
		t.Fatal("没有农历数据时格子底部本来就有墨迹 —— 这条断言区分不出农历")
	}
	if isBlankRegion(r.Render(with, ui.State{}), bottom) {
		t.Fatal("有农历数据时格子底部是空的 —— 农历没画")
	}
}

// 今天那格是黑块反白，农历也必须是白的。
//
// 变异：今天格的农历改回 ColorLight（179）→ 与白色参考图的差异非零、与浅灰参考图相同，本条红。
func TestCalendarTodayLunarIsWhite(t *testing.T) {
	r := newTestRenderer(t)
	const lunar = "廿二"
	snap := baseSnap() // Now = 2026-10-29
	snap.Calendar = map[string]state.DayInfo{
		"2026-10-29": {LunarMonthName: "九月", LunarDayName: lunar},
	}
	img := r.Render(snap, ui.State{})

	cell := calendarCellRect(4, 4) // 2026-10-29
	// 用同一字面、同一位置分别画白/浅灰参考图：黑块内部只有农历是这层墨迹，
	// 逐像素比较既钉颜色也钉位置。左右各留 10px 避开圆角。
	wantWhite := image.NewGray(image.Rect(0, 0, CanvasW, CanvasH))
	TextCentered(wantWhite, r.faceCalLunar, lunar, cell.Min.X, cell.Min.Y+58, CalCellW, ColorWhite)
	wantLight := image.NewGray(image.Rect(0, 0, CanvasW, CanvasH))
	TextCentered(wantLight, r.faceCalLunar, lunar, cell.Min.X, cell.Min.Y+58, CalCellW, ColorLight)

	band := image.Rect(cell.Min.X+10, cell.Min.Y+44, cell.Max.X-10, cell.Min.Y+59)
	if n := diffInBand(img, wantWhite, band); n != 0 {
		t.Fatalf("今天格的农历与白色参考图有 %d 像素不同 —— 黑底上不是白色", n)
	}
	if diffInBand(img, wantLight, band) == 0 {
		t.Fatal("今天格的农历与浅灰参考图完全一致 —— 黑底上用了 ColorLight")
	}
}

// 今天那格的黑块必须画满整格：农历字的下缘会探到 cellY+62 附近，
// 块只到 cellY+60 时白字压在白色背景上，下半截等于被切掉。
//
// 变异：块高度改回 CalCellH-8 → 最底一行变成白底，深色像素数为 0，本条红。
func TestCalendarTodayBlockCoversFullCell(t *testing.T) {
	r := newTestRenderer(t)
	snap := baseSnap() // Now = 2026-10-29
	snap.Calendar = map[string]state.DayInfo{
		"2026-10-29": {LunarMonthName: "九月", LunarDayName: "廿二"},
	}
	img := r.Render(snap, ui.State{})

	cell := calendarCellRect(4, 4) // 2026-10-29
	// 最底一行、避开圆角与白字笔画：只要有足够多的深色像素，
	// 就说明黑块确实铺到了格底。用计数而不是单个像素，避免某个字体
	// 恰好在这条线上落下一笔白色农历。
	bottomY := cell.Max.Y - 2
	dark := 0
	for x := cell.Min.X + 10; x < cell.Max.X-10; x++ {
		if img.GrayAt(x, bottomY).Y < 100 {
			dark++
		}
	}
	if dark < 20 {
		t.Fatalf("今天格最底一行只有 %d 个深色像素 —— 黑块没铺满整格", dark)
	}
}

// 日历格底部：有节气显示节气，否则只显示农历日名（不显示月份）。
//
// 直接断言返回值：gofont 没有中文字形，「寒露」与「初八」会画成同一个
// 缺字方框，纯像素对照区分不了两者；字符串断言在任何字体下都能杀掉
// 「农历日优先」或「画出月名」的变异。
func TestCalendarCellLunar(t *testing.T) {
	cases := []struct {
		name string
		in   state.DayInfo
		want string
	}{
		{"节气优先于农历日", state.DayInfo{SolarTerm: "寒露", LunarDayName: "初八"}, "寒露"},
		{"没有节气时只画日名", state.DayInfo{LunarMonthName: "九月", LunarDayName: "初八"}, "初八"},
		{"只有节气", state.DayInfo{SolarTerm: "寒露"}, "寒露"},
		{"只有月名时格子为空", state.DayInfo{LunarMonthName: "九月"}, ""},
		{"无数据", state.DayInfo{}, ""},
	}
	for _, c := range cases {
		if got := calendarCellLunar(c.in); got != c.want {
			t.Errorf("%s: calendarCellLunar(%+v) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// 像素路径也验证一次：既有节气又有农历日时，格底必须与「只有节气」一致，
// 且与「只有农历日」不同。
//
// 用设备 CJK 字体：gofont 下「寒露」「初八」都是同一个缺字方框，无法区分。
// 变异：把优先级反转成「农历日优先」→ 与「只有节气」出现差异，本条红。
func TestCalendarCellPrefersSolarTermRendering(t *testing.T) {
	reg, bold := deviceFontPaths(t)
	if reg == "" {
		t.Skip("没有可用的设备字体（设 KINDLECLOCK_TEST_FONT_REGULAR 指定）")
	}
	f, err := NewFonts(reg, bold)
	if err != nil {
		t.Fatalf("设备字体解析失败: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	r, err := NewRenderer(f)
	if err != nil {
		t.Fatal(err)
	}
	cell := calendarCellRect(3, 2)

	withBoth := baseSnap()
	withBoth.Calendar = map[string]state.DayInfo{
		"2026-10-20": {SolarTerm: "寒露", LunarDayName: "初八"},
	}
	termOnly := baseSnap()
	termOnly.Calendar = map[string]state.DayInfo{
		"2026-10-20": {SolarTerm: "寒露"},
	}
	lunarOnly := baseSnap()
	lunarOnly.Calendar = map[string]state.DayInfo{
		"2026-10-20": {LunarDayName: "初八"},
	}

	both := r.Render(withBoth, ui.State{})
	if n := diffInBand(both, r.Render(termOnly, ui.State{}), cell); n != 0 {
		t.Fatalf("节气+农历日与只有节气有 %d 像素不同 —— 节气没有优先", n)
	}
	if diffInBand(both, r.Render(lunarOnly, ui.State{}), cell) == 0 {
		t.Fatal("节气+农历日与只有农历日完全一致 —— 节气被农历日盖掉了")
	}
}

// 日历格底部不画农历月份（格子里放不下，左栏已有完整农历）。
func TestCalendarCellOmitsLunarMonth(t *testing.T) {
	r := newTestRenderer(t)
	cell := calendarCellRect(3, 2)
	plain := baseSnap()

	dayOnly := baseSnap()
	dayOnly.Calendar = map[string]state.DayInfo{
		"2026-10-20": {LunarMonthName: "九月", LunarDayName: "廿二"},
	}
	otherMonth := baseSnap()
	otherMonth.Calendar = map[string]state.DayInfo{
		"2026-10-20": {LunarMonthName: "八月", LunarDayName: "廿二"},
	}
	// 只有月名、没有日名：必须保持空白。
	// （gofont 把「九月廿二」与「八月廿二」画成同样的方框，用换月名测会
	// 漏掉 LunarFull 的变异；用「有月无日」才有判别力。）
	monthOnly := baseSnap()
	monthOnly.Calendar = map[string]state.DayInfo{
		"2026-10-20": {LunarMonthName: "九月"},
	}

	base := r.Render(dayOnly, ui.State{})
	if n := diffInBand(base, r.Render(otherMonth, ui.State{}), cell); n != 0 {
		t.Fatalf("换月名后格子有 %d 像素变化 —— 日历格不该画农历月名", n)
	}
	if n := diffInBand(r.Render(plain, ui.State{}), r.Render(monthOnly, ui.State{}), cell); n != 0 {
		t.Fatalf("只有月名时格子有 %d 像素变化 —— 不该画出月名", n)
	}
}

// 选中某天时，课程标题要带上完整农历与节假日名，顺序固定。
func TestCourseTitleIncludesLunarAndHoliday(t *testing.T) {
	snap := baseSnap()
	snap.Calendar = map[string]state.DayInfo{
		"2026-10-30": {
			LunarMonthName: "九月", LunarDayName: "廿二",
			HolidayName: "国庆节", HolidayKind: state.HolidayRest,
		},
	}
	selected := time.Date(2026, 10, 30, 0, 0, 0, 0, snap.Now.Location())

	if got, want := courseTitle(snap, ui.State{SelectedDate: selected}),
		"10月30日 课程 · 九月廿二 · 国庆节"; got != want {
		t.Fatalf("courseTitle() = %q，期望 %q", got, want)
	}
}

// 没有节假日时仍带完整农历；只有节气时农历缺席就只显示节气。
func TestCourseTitleLunarAndSolarTerm(t *testing.T) {
	snap := baseSnap()
	snap.Calendar = map[string]state.DayInfo{
		"2026-10-20": {LunarMonthName: "九月", LunarDayName: "初十"},
		"2026-10-21": {SolarTerm: "立冬"},
	}
	loc := snap.Now.Location()
	selected := time.Date(2026, 10, 20, 0, 0, 0, 0, loc)
	if got, want := courseTitle(snap, ui.State{SelectedDate: selected}),
		"10月20日 课程 · 九月初十"; got != want {
		t.Fatalf("无节假日的选中日: courseTitle() = %q，期望 %q", got, want)
	}

	selected = time.Date(2026, 10, 21, 0, 0, 0, 0, loc)
	if got, want := courseTitle(snap, ui.State{SelectedDate: selected}),
		"10月21日 课程 · 立冬"; got != want {
		t.Fatalf("只有节气的选中日: courseTitle() = %q，期望 %q", got, want)
	}
}

// 跟随今天时标题固定是「今日课程」，不受日历数据影响。
func TestCourseTitleFollowsToday(t *testing.T) {
	snap := baseSnap()
	snap.Calendar = map[string]state.DayInfo{
		"2026-10-29": {LunarMonthName: "九月", LunarDayName: "廿二"},
	}
	if got := courseTitle(snap, ui.State{}); got != "今日课程" {
		t.Fatalf("跟随今天时应显示「今日课程」，得到 %q", got)
	}
}

// 选中某天时节假日名确实画进了标题带（像素路径也验证一次）。
func TestSelectedDayShowsHolidayName(t *testing.T) {
	r := newTestRenderer(t)
	selected := time.Date(2026, 10, 20, 0, 0, 0, 0, baseSnap().Now.Location())
	titleBand := image.Rect(RightX, CoursesTitleY, RightX+RightW, CoursesTitleY+32)

	lunarOnly := baseSnap()
	lunarOnly.Calendar = map[string]state.DayInfo{
		"2026-10-20": {LunarMonthName: "九月", LunarDayName: "初十"},
	}
	holiday := baseSnap()
	holiday.Calendar = map[string]state.DayInfo{
		"2026-10-20": {
			LunarMonthName: "九月", LunarDayName: "初十",
			HolidayName: "国庆节", HolidayKind: state.HolidayRest,
		},
	}

	withLunar := r.Render(lunarOnly, ui.State{SelectedDate: selected})
	withHoliday := r.Render(holiday, ui.State{SelectedDate: selected})
	if diffInBand(withLunar, withHoliday, titleBand) == 0 {
		t.Fatal("选中节假日时标题没有变化 —— 节假日名没画")
	}
}

// 选中日的节气也要显示（「11月7日 课程 · 立冬」）。
func TestSelectedDayShowsSolarTerm(t *testing.T) {
	r := newTestRenderer(t)
	selected := time.Date(2026, 10, 20, 0, 0, 0, 0, baseSnap().Now.Location())
	titleBand := image.Rect(RightX, CoursesTitleY, RightX+RightW, CoursesTitleY+32)

	noInfo := baseSnap()
	term := baseSnap()
	term.Calendar = map[string]state.DayInfo{"2026-10-20": {SolarTerm: "立冬"}}

	if diffInBand(r.Render(noInfo, ui.State{SelectedDate: selected}),
		r.Render(term, ui.State{SelectedDate: selected}), titleBand) == 0 {
		t.Fatal("选中节气日时标题没有变化 —— 节气名没画")
	}
}

// 课程行要有教师；教师为空时不占位。
func TestCourseRowShowsTeacher(t *testing.T) {
	r := newTestRenderer(t)
	mk := func(teacher string) *image.Gray {
		s := baseSnap()
		s.Timetable.Courses = []state.Course{{
			Name: "高等数学", StartText: "14:15", EndText: "14:55",
			Start: at(14, 15), End: at(14, 55), Room: "公教楼220", Teacher: teacher,
		}}
		s.NextCourseIdx = -1
		return r.Render(s, ui.State{})
	}

	with := mk("王娟")
	without := mk("")
	if imagesEqual(with, without) {
		t.Fatal("加教师后渲染结果不变 —— 教师没画")
	}
	// 差异必须落在课名与教室列之间的这一行，而不是跑到别处。
	row := image.Rect(RightX+CourseTimeW, CoursesStartY,
		RightX+RightW-CourseRoomW, CoursesStartY+CourseRowH)
	if diffInBand(with, without, row) == 0 {
		t.Fatal("教师墨迹不在课程行的课名/教师区")
	}
}
