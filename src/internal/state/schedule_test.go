package state

import (
	"testing"
	"time"

	"github.com/rhsy/kindleclock/src/internal/config"
)

func sched() *Schedule {
	return &Schedule{
		// FetchedAt 是周次推算的锚点，缺了它 WeekOf 会算出一个天文数字。
		FetchedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		Week:      10, MaxWeek: 19,
		Specs: []CourseSpec{
			{Name: "高数", DayOfWeek: 5, StartSection: 5, EndSection: 6, Weeks: []int{10, 11}},
			{Name: "物理", DayOfWeek: 5, StartSection: 1, EndSection: 2, Weeks: []int{10, 11}},
			{Name: "只在第11周", DayOfWeek: 5, StartSection: 9, EndSection: 9, Weeks: []int{11}},
		},
	}
}

var periods = []config.Period{
	{StartMin: 8*60 + 20, EndMin: 9 * 60}, // 1-2 节的第一节
	{StartMin: 9*60 + 5, EndMin: 9*60 + 45},
	{StartMin: 10 * 60, EndMin: 10*60 + 40},
	{StartMin: 10*60 + 45, EndMin: 11*60 + 25},
	{StartMin: 13*60 + 30, EndMin: 14*60 + 10}, // 第 5 节
	{StartMin: 14*60 + 15, EndMin: 14*60 + 55},
	{StartMin: 15*60 + 10, EndMin: 15*60 + 50},
	{StartMin: 15*60 + 55, EndMin: 16*60 + 35},
	{StartMin: 18 * 60, EndMin: 18*60 + 40},
}

// 周次推算：以 Schedule.Week 为锚点，按「与今天相隔几个周一」偏移。
func TestWeekOf(t *testing.T) {
	s := sched()
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) // 周五
	cases := []struct {
		date time.Time
		want int
	}{
		{today, 10},
		{today.AddDate(0, 0, -4), 10}, // 同周周一
		{today.AddDate(0, 0, 2), 10},  // 同周周日（today 是周五，+2 才是周日；+3 已是下周一）
		{today.AddDate(0, 0, 4), 11},  // 下周一
		{today.AddDate(0, 0, -7), 9},
		{today.AddDate(0, 0, 35), 15},
	}
	for _, c := range cases {
		if got := WeekOf(s, c.date); got != c.want {
			t.Errorf("%s: WeekOf = %d，期望 %d", c.date.Format("01-02"), got, c.want)
		}
	}
}

func TestCoursesForDayFiltersByWeek(t *testing.T) {
	s := sched()
	friday := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	got := CoursesForDay(s, friday, periods)
	if len(got) != 2 {
		t.Fatalf("第 10 周周五应有 2 门课，得到 %d: %+v", len(got), got)
	}
	// 必须按开始时间升序
	if got[0].Name != "物理" || got[1].Name != "高数" {
		t.Fatalf("应按节次排序，得到 %+v", got)
	}
	if got[0].StartText != "08:20" || got[0].EndText != "09:45" {
		t.Fatalf("时间换算错: %s-%s", got[0].StartText, got[0].EndText)
	}
	if !got[0].Start.Equal(time.Date(2026, 10, 30, 8, 20, 0, 0, time.UTC)) {
		t.Fatalf("Start 应为 08:20 UTC，得到 %v", got[0].Start)
	}
	if !got[0].End.Equal(time.Date(2026, 10, 30, 9, 45, 0, 0, time.UTC)) {
		t.Fatalf("End 应为 09:45 UTC，得到 %v", got[0].End)
	}
}

// 混用时区时周次必须按日历日算：周一 11-02 00:00 +08 与锚点
// 10-30 12:00 UTC 只隔一个周一，不能被绝对时长截断成同一周。
func TestWeeksBetweenMixedTimezones(t *testing.T) {
	utcMonday := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC) // 锚点所在周一
	cst := time.FixedZone("CST", 8*3600)
	cstMonday := time.Date(2026, 10, 26, 0, 0, 0, 0, cst)
	if got := weeksBetween(utcMonday, cstMonday); got != 0 {
		t.Errorf("同一日历周：weeksBetween = %d，期望 0", got)
	}
	nextCSTMonday := time.Date(2026, 11, 2, 0, 0, 0, 0, cst)
	if got := weeksBetween(utcMonday, nextCSTMonday); got != 1 {
		t.Errorf("下一周：weeksBetween = %d，期望 1", got)
	}
	if got := WeekOf(sched(), nextCSTMonday); got != 11 {
		t.Errorf("混时区 WeekOf = %d，期望 11", got)
	}
}

// date 在 +08 时区时，Start/End 必须落在该时区，墙钟文本仍按 date 的
// Location 解释；仅断言文本无法发现整体偏移 8 小时。
func TestCoursesForDayTimezone(t *testing.T) {
	s := sched()
	cst := time.FixedZone("CST", 8*3600)
	friday := time.Date(2026, 10, 30, 8, 0, 0, 0, cst)
	got := CoursesForDay(s, friday, periods)
	if len(got) != 2 {
		t.Fatalf("+08 时区第 10 周周五应有 2 门课，得到 %d: %+v", len(got), got)
	}
	if got[0].Name != "物理" {
		t.Fatalf("应按开始时间排序，得到 %+v", got)
	}
	if got[0].StartText != "08:20" || got[0].EndText != "09:45" {
		t.Fatalf("时间文本错: %s-%s", got[0].StartText, got[0].EndText)
	}
	if got[0].Start.Location() != cst || got[0].End.Location() != cst {
		t.Fatalf("Start/End 应落在 date 的时区，得到 %v / %v",
			got[0].Start.Location(), got[0].End.Location())
	}
	wantStart := time.Date(2026, 10, 30, 8, 20, 0, 0, cst)
	wantEnd := time.Date(2026, 10, 30, 9, 45, 0, 0, cst)
	if !got[0].Start.Equal(wantStart) || !got[0].End.Equal(wantEnd) {
		t.Fatalf("Start/End = %v/%v，期望 %v/%v", got[0].Start, got[0].End, wantStart, wantEnd)
	}
}

func TestCoursesForDayNextWeek(t *testing.T) {
	s := sched()
	nextFriday := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	got := CoursesForDay(s, nextFriday, periods)
	if len(got) != 3 {
		t.Fatalf("第 11 周周五应有 3 门课，得到 %d: %+v", len(got), got)
	}
}

func TestCoursesForDayNonTeachingWeek(t *testing.T) {
	s := sched()
	far := time.Date(2027, 3, 5, 8, 0, 0, 0, time.UTC) // 远超 maxWeek
	if got := CoursesForDay(s, far, periods); len(got) != 0 {
		t.Fatalf("非教学周不该有课，得到 %+v", got)
	}
}

// 周次超出 MaxWeek 时必须返回空 —— 即使那周确实有 spec 匹配。
// 删掉 MaxWeek 守卫后本用例会红。
func TestCoursesForDayRespectsMaxWeek(t *testing.T) {
	s := &Schedule{
		FetchedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		Week:      10, MaxWeek: 19,
		Specs: []CourseSpec{
			{Name: "第20周的课", DayOfWeek: 5, StartSection: 1, EndSection: 1, Weeks: []int{20}},
		},
	}
	friday20 := time.Date(2027, 1, 8, 8, 0, 0, 0, time.UTC) // 第 20 周周五（已核对）
	if got := CoursesForDay(s, friday20, periods); len(got) != 0 {
		t.Fatalf("超出 MaxWeek 应返回空，得到 %+v", got)
	}
}

// 周日：Weekday 的 0 要映射成业务里的 7。
func TestCoursesForDaySunday(t *testing.T) {
	s := &Schedule{
		FetchedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		Week:      10, MaxWeek: 19,
		Specs: []CourseSpec{
			{Name: "周日课", DayOfWeek: 7, StartSection: 1, EndSection: 1, Weeks: []int{10}},
		},
	}
	sunday := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC) // 第 10 周周日
	got := CoursesForDay(s, sunday, periods)
	if len(got) != 1 || got[0].Name != "周日课" {
		t.Fatalf("周日应匹配 DayOfWeek=7，得到 %+v", got)
	}
}

// 节次超出 periods 范围时跳过那门课，不能让整天的课都消失。
func TestCoursesForDaySkipsOutOfRangeSections(t *testing.T) {
	s := &Schedule{
		FetchedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		Week:      10, MaxWeek: 19, Specs: []CourseSpec{
			{Name: "正常", DayOfWeek: 5, StartSection: 1, EndSection: 1, Weeks: []int{10}},
			{Name: "起始为0", DayOfWeek: 5, StartSection: 0, EndSection: 1, Weeks: []int{10}},
			{Name: "起止颠倒", DayOfWeek: 5, StartSection: 3, EndSection: 2, Weeks: []int{10}},
			{Name: "节次越界", DayOfWeek: 5, StartSection: 30, EndSection: 31, Weeks: []int{10}},
		}}
	friday := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	got := CoursesForDay(s, friday, periods)
	if len(got) != 1 || got[0].Name != "正常" {
		t.Fatalf("应只保留节次可换算的那门，得到 %+v", got)
	}
}
