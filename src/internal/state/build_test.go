package state

import (
	"fmt"
	"testing"
	"time"

	"github.com/rhsy/kindleclock/src/internal/config"
)

var loc = time.FixedZone("CST", 8*3600)

func at(h, m int) time.Time {
	return time.Date(2026, 10, 29, h, m, 0, 0, loc) // 2026-10-29 是星期四
}

func mkCourse(name, startT, endT string) Course {
	st, _ := time.ParseInLocation("15:04", startT, loc)
	en, _ := time.ParseInLocation("15:04", endT, loc)
	st = time.Date(2026, 10, 29, st.Hour(), st.Minute(), 0, 0, loc)
	en = time.Date(2026, 10, 29, en.Hour(), en.Minute(), 0, 0, loc)
	return Course{Name: name, StartText: startT, EndText: endT, Start: st, End: en}
}

func TestBuildFiltersFinishedCourses(t *testing.T) {
	now := at(15, 0)
	tt := Timetable{
		Available: true,
		Courses: []Course{
			mkCourse("早课", "08:20", "09:45"),
			mkCourse("刚下课", "14:15", "14:55"),
			mkCourse("下一节", "15:10", "15:50"),
			mkCourse("晚课", "18:00", "18:40"),
		},
	}
	snap := Build(now, nil, tt, nil, time.Hour)

	if len(snap.Timetable.Courses) != 2 {
		t.Fatalf("期望保留 2 门剩余课，得到 %d: %+v", len(snap.Timetable.Courses), snap.Timetable.Courses)
	}
	if snap.Timetable.Courses[0].Name != "下一节" {
		t.Errorf("第一门应是「下一节」，得到 %q", snap.Timetable.Courses[0].Name)
	}
	if snap.Timetable.Courses[1].Name != "晚课" {
		t.Errorf("第二门应是「晚课」，得到 %q", snap.Timetable.Courses[1].Name)
	}
}

func TestBuildKeepsCourseInProgress(t *testing.T) {
	now := at(15, 30) // 正在上 15:10-15:50
	tt := Timetable{
		Available: true,
		Courses:   []Course{mkCourse("正在上", "15:10", "15:50")},
	}
	snap := Build(now, nil, tt, nil, time.Hour)
	if len(snap.Timetable.Courses) != 1 {
		t.Fatalf("正在上的课不应被过滤，得到 %d 门", len(snap.Timetable.Courses))
	}
	if snap.NextCourseIdx != 0 {
		t.Errorf("NextCourseIdx = %d, 期望 0", snap.NextCourseIdx)
	}
}

func TestBuildNextCourseIdx(t *testing.T) {
	now := at(15, 0)
	tt := Timetable{
		Available: true,
		Courses: []Course{
			mkCourse("下一节", "15:10", "15:50"),
			mkCourse("晚课", "18:00", "18:40"),
		},
	}
	snap := Build(now, nil, tt, nil, time.Hour)
	if snap.NextCourseIdx != 0 {
		t.Errorf("NextCourseIdx = %d, 期望 0（第一门就是下一节）", snap.NextCourseIdx)
	}
}

func TestBuildNoCoursesLeft(t *testing.T) {
	now := at(21, 30)
	tt := Timetable{
		Available: true,
		Courses:   []Course{mkCourse("晚课", "18:00", "18:40")},
		Message:   "今日暂无后续课程",
	}
	snap := Build(now, nil, tt, nil, time.Hour)
	if len(snap.Timetable.Courses) != 0 {
		t.Fatalf("应无剩余课程，得到 %d", len(snap.Timetable.Courses))
	}
	if snap.Timetable.Message != "今日暂无后续课程" {
		t.Errorf("Message = %q", snap.Timetable.Message)
	}
	if snap.NextCourseIdx != -1 {
		t.Errorf("NextCourseIdx = %d, 期望 -1", snap.NextCourseIdx)
	}
}

func TestBuildMarksSectionLabel(t *testing.T) {
	now := at(15, 0)
	periods := []config.Period{
		{StartMin: 14*60 + 15, EndMin: 14*60 + 55}, // 第 1 节
		{StartMin: 15*60 + 10, EndMin: 15*60 + 50}, // 第 2 节
	}
	tt := Timetable{Available: true, Courses: []Course{mkCourse("课", "15:10", "15:50")}}
	snap := Build(now, nil, tt, periods, time.Hour)
	if snap.Timetable.Courses[0].Section != "2节" {
		t.Errorf("Section = %q, 期望 2节", snap.Timetable.Courses[0].Section)
	}
}

func TestBuildWeatherFreshness(t *testing.T) {
	now := at(15, 0)
	fresh := &Weather{FetchedAt: now.Add(-30 * time.Minute)}
	stale := &Weather{FetchedAt: now.Add(-3 * time.Hour)}

	if snap := Build(now, fresh, Timetable{}, nil, time.Hour); !snap.WeatherFresh {
		t.Error("30 分钟前的天气应算新鲜")
	}
	if snap := Build(now, stale, Timetable{}, nil, time.Hour); snap.WeatherFresh {
		t.Error("3 小时前的天气应算陈旧")
	}
	if snap := Build(now, nil, Timetable{}, nil, time.Hour); snap.Weather != nil || snap.WeatherFresh {
		t.Error("无天气时 Weather 应为 nil、WeatherFresh 应为 false")
	}
}

func TestBuildTimetableUnavailable(t *testing.T) {
	now := at(15, 0)
	// 不可用时即便带着上次成功的课程列表（last-good），也不能显示
	tt := Timetable{
		Available: false,
		Message:   "课表暂不可用",
		Courses: []Course{
			mkCourse("下一节", "15:10", "15:50"),
			mkCourse("晚课", "18:00", "18:40"),
		},
	}
	snap := Build(now, nil, tt, nil, time.Hour)
	if snap.Timetable.Available {
		t.Error("Available 应为 false")
	}
	if len(snap.Timetable.Courses) != 0 {
		t.Errorf("不可用时不应给出课程，得到 %d 门", len(snap.Timetable.Courses))
	}
	if snap.NextCourseIdx != -1 {
		t.Errorf("不可用时 NextCourseIdx 应为 -1，得到 %d", snap.NextCourseIdx)
	}
}

func TestBuildSortsCoursesByStart(t *testing.T) {
	now := at(15, 0)
	tt := Timetable{
		Available: true,
		Courses: []Course{
			mkCourse("晚课", "18:00", "18:40"),
			mkCourse("下一节", "15:10", "15:50"),
		},
	}
	snap := Build(now, nil, tt, nil, time.Hour)
	if len(snap.Timetable.Courses) != 2 {
		t.Fatalf("应保留 2 门课，得到 %d", len(snap.Timetable.Courses))
	}
	if snap.Timetable.Courses[0].Name != "下一节" || snap.Timetable.Courses[1].Name != "晚课" {
		t.Errorf("课程应按开始时间升序，得到 %q, %q",
			snap.Timetable.Courses[0].Name, snap.Timetable.Courses[1].Name)
	}
	if !snap.Timetable.Courses[0].Start.Before(snap.Timetable.Courses[1].Start) {
		t.Error("排序后第一门的开始时间必须早于第二门")
	}
	if snap.NextCourseIdx != 0 {
		t.Errorf("NextCourseIdx = %d, 期望 0（排序后 15:10 那门）", snap.NextCourseIdx)
	}
}

func TestBuildPrefersUpcomingOverInProgress(t *testing.T) {
	now := at(15, 30)
	tt := Timetable{
		Available: true,
		Courses: []Course{
			mkCourse("正在上", "15:10", "15:50"),
			mkCourse("晚课", "18:00", "18:40"),
		},
	}
	snap := Build(now, nil, tt, nil, time.Hour)
	if len(snap.Timetable.Courses) != 2 {
		t.Fatalf("应保留 2 门课，得到 %d", len(snap.Timetable.Courses))
	}
	if snap.NextCourseIdx != 1 {
		t.Errorf("NextCourseIdx = %d, 期望 1（优先未开始的 18:00 那门）", snap.NextCourseIdx)
	}
}

func TestBuildWeatherFreshnessBoundary(t *testing.T) {
	now := at(15, 0)
	// 恰好等于阈值：<= 应算新鲜
	edge := &Weather{FetchedAt: now.Add(-time.Hour)}
	if snap := Build(now, edge, Timetable{}, nil, time.Hour); !snap.WeatherFresh {
		t.Error("距抓取恰好 1 小时、阈值 1 小时时应算新鲜（<= 而非 <）")
	}
}

func TestBuildFiltersCourseEndingExactlyNow(t *testing.T) {
	now := at(15, 50)
	tt := Timetable{
		Available: true,
		Courses:   []Course{mkCourse("刚结束", "15:10", "15:50")},
	}
	snap := Build(now, nil, tt, nil, time.Hour)
	if len(snap.Timetable.Courses) != 0 {
		t.Errorf("结束时间恰好等于 now 的课应被过滤，得到 %d 门", len(snap.Timetable.Courses))
	}
	if snap.NextCourseIdx != -1 {
		t.Errorf("NextCourseIdx = %d, 期望 -1", snap.NextCourseIdx)
	}
}

func TestBuildDoesNotMutateInput(t *testing.T) {
	now := at(15, 0)
	// 夹具刻意乱序（18:00 在 15:10 之前）：若 Build 为了排序而就地重排入参，
	// 这个顺序会被改掉，测试才能发现
	orig := []Course{
		mkCourse("晚课", "18:00", "18:40"),
		mkCourse("早课", "15:10", "15:50"),
	}
	tt := Timetable{Available: true, Courses: orig}
	snap := Build(now, nil, tt, nil, time.Hour)

	// 长度、顺序、每个元素的可见字段都不应变
	if len(orig) != 2 {
		t.Fatalf("Build 不应修改输入切片，现在长度 %d", len(orig))
	}
	if orig[0].Name != "晚课" || orig[1].Name != "早课" {
		t.Errorf("Build 不应重排输入切片，现在顺序是 %q, %q", orig[0].Name, orig[1].Name)
	}
	want := []struct{ name, start, end string }{
		{"晚课", "18:00", "18:40"},
		{"早课", "15:10", "15:50"},
	}
	for i, w := range want {
		c := orig[i]
		if c.Name != w.name || c.StartText != w.start || c.EndText != w.end || c.Section != "" {
			t.Errorf("输入 Courses[%d] 被修改: %+v，期望 Name=%q StartText=%q EndText=%q Section=\"\"",
				i, c, w.name, w.start, w.end)
		}
	}

	// 输出必须是新的底层数组，不能与入参共享（排序会移动元素，故两两比对地址）
	for i := range snap.Timetable.Courses {
		for j := range orig {
			if &snap.Timetable.Courses[i] == &orig[j] {
				t.Errorf("输出 Courses[%d] 与入参 Courses[%d] 共享同一底层数组，Build 必须复制",
					i, j)
			}
		}
	}
}

// Section 回填必须只发生在输出上，不能写回入参元素
func TestBuildDoesNotWriteSectionBackToInput(t *testing.T) {
	now := at(15, 0)
	periods := []config.Period{
		{StartMin: 15*60 + 10, EndMin: 15*60 + 50},
	}
	orig := []Course{mkCourse("课", "15:10", "15:50")}
	if orig[0].Section != "" {
		t.Fatal("夹具的 Section 应初始为空")
	}

	snap := Build(now, nil, Timetable{Available: true, Courses: orig}, periods, time.Hour)

	if snap.Timetable.Courses[0].Section != "1节" {
		t.Errorf("输出应回填节次，得到 %q", snap.Timetable.Courses[0].Section)
	}
	if orig[0].Section != "" {
		t.Errorf("入参的 Section 被写回了: %q", orig[0].Section)
	}
}

// SliceStable 的稳定性：同时开始的课必须保持上游顺序。
//
// 夹具需要 13 门课，不能只放 3 门：sort.Slice 对 n <= 12 走插入排序，
// 而插入排序本身就是稳定的，小夹具无法区分 Slice 与 SliceStable
// （实测 3~12 门全部同 Start 时，两者输出一致）。n >= 13 才会走
// pdqsort 的分区，稳定性才真正成为可观测的性质。
func TestBuildSortIsStableForEqualStart(t *testing.T) {
	now := at(7, 0)
	// 两档开始时间交错排列，保持各自的上游相对顺序
	starts := []string{
		"08:20", "08:00", "08:20", "08:20", "08:20", "08:00", "08:00",
		"08:20", "08:20", "08:00", "08:00", "08:00", "08:00",
	}
	courses := make([]Course, len(starts))
	for i, s := range starts {
		courses[i] = mkCourse(fmt.Sprintf("课%02d", i), s, "09:45")
	}
	snap := Build(now, nil, Timetable{Available: true, Courses: courses}, nil, time.Hour)

	if len(snap.Timetable.Courses) != len(starts) {
		t.Fatalf("应保留 %d 门课，得到 %d", len(starts), len(snap.Timetable.Courses))
	}

	// 先按开始时间分组，组内必须仍是上游顺序
	var gotEarly, gotLate []string
	for _, c := range snap.Timetable.Courses {
		switch c.StartText {
		case "08:00":
			gotEarly = append(gotEarly, c.Name)
		case "08:20":
			gotLate = append(gotLate, c.Name)
		default:
			t.Fatalf("出现意外的开始时间 %q", c.StartText)
		}
	}
	wantEarly := []string{"课01", "课05", "课06", "课09", "课10", "课11", "课12"}
	wantLate := []string{"课00", "课02", "课03", "课04", "课07", "课08"}

	if !equalStrings(gotEarly, wantEarly) {
		t.Errorf("08:00 的课应保持上游顺序：得到 %v，期望 %v", gotEarly, wantEarly)
	}
	if !equalStrings(gotLate, wantLate) {
		t.Errorf("08:20 的课应保持上游顺序：得到 %v，期望 %v", gotLate, wantLate)
	}

	// 整体仍须按开始时间升序：全部 08:00 排在全部 08:20 之前
	if len(gotEarly) > 0 && snap.Timetable.Courses[0].StartText != "08:00" {
		t.Errorf("排序后第一门应是 08:00，得到 %q", snap.Timetable.Courses[0].StartText)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
