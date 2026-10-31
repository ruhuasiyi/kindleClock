package ui

import (
	"testing"
	"time"
)

func TestStateDefaults(t *testing.T) {
	var s State
	if !s.FollowToday() || !s.IsDefault() {
		t.Fatal("零值应当是「跟随今天」的默认态")
	}
}

func TestStateReset(t *testing.T) {
	s := State{
		SelectedDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		MonthOffset:  1,
		CourseScroll: 2,
		SettingsOpen: true,
	}
	s.Reset()
	if !s.IsDefault() {
		t.Fatalf("Reset 之后应回到默认态，得到 %+v", s)
	}
}

// 60 秒无交互自动回位 —— 但只有非默认态才需要。
//
// 返回值必须如实反映「这一轮是否回位」：主循环靠它决定要不要强制整屏
// 刷新。恒返回 false 会让回位后的第一帧只做局部刷新，残留别的月份。
func TestIdleReset(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	s := State{MonthOffset: 1, LastInput: now}
	if got := s.ResetIfIdle(now.Add(59*time.Second), 60*time.Second); got {
		t.Fatal("59 秒还没回位，不该报告回位")
	}
	if s.MonthOffset != 1 {
		t.Fatal("59 秒还不该回位")
	}
	if got := s.ResetIfIdle(now.Add(61*time.Second), 60*time.Second); !got {
		t.Fatal("61 秒回位了，应报告 true")
	}
	if s.MonthOffset != 0 {
		t.Fatal("61 秒应当回位")
	}
	// 已经是默认态：不应该再报告一次回位，否则主循环会白做一次全屏刷新。
	if got := s.ResetIfIdle(now.Add(10*time.Minute), 60*time.Second); got {
		t.Fatal("默认态不该报告回位")
	}
}

// 从未交互过（LastInput 为零）时不该回位，更不该报告回位。
func TestIdleResetNeverTouched(t *testing.T) {
	var s State
	if got := s.ResetIfIdle(time.Now(), 60*time.Second); got {
		t.Fatal("从未交互过不该报告回位")
	}
}

// 月份起点必须从「当月 1 日」做月份运算，不能拿今天直接 AddDate。
//
// 反例：10-31 加一个月是 12-01，直接跳过 11 月 —— 标题会显示 12 月，
// 而命中测试用的是同一个月起点，两边一起错，用户点哪都不对。
func TestMonthStartNeverOverflows(t *testing.T) {
	cases := []struct {
		today  time.Time
		offset int
		want   string
	}{
		{time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), 1, "2026-11-01"},
		{time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), 0, "2026-10-01"},
		{time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), -1, "2026-09-01"},
		{time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC), 1, "2027-01-01"},
		{time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC), -1, "2025-12-01"},
	}
	for _, c := range cases {
		got := MonthStart(c.today, c.offset).Format("2006-01-02")
		if got != c.want {
			t.Errorf("%s + %d 月: 得到 %s，期望 %s",
				c.today.Format("2006-01-02"), c.offset, got, c.want)
		}
	}
}

// 月份起点应当保留调用方时区的午夜 —— 后面所有格子日期都从它派生。
func TestMonthStartKeepsLocation(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	got := MonthStart(time.Date(2026, 10, 31, 12, 30, 0, 0, loc), 1)
	if got.Location() != loc {
		t.Errorf("时区 = %v，期望 %v", got.Location(), loc)
	}
	if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
		t.Errorf("时间 = %02d:%02d:%02d，期望午夜", h, m, s)
	}
}

// 触屏时月份偏移要跟着选中日期走，否则会出现「选中 11 月 2 日但日历显示 10 月」。
func TestSelectDateUpdatesMonthOffset(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	s := State{}
	s.SelectDate(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), today)
	if s.MonthOffset != 1 {
		t.Fatalf("选中 11 月应把月份偏移设成 1，得到 %d", s.MonthOffset)
	}
	s.SelectDate(today, today)
	if !s.FollowToday() || s.MonthOffset != 0 {
		t.Fatalf("选中今天应回到跟随态，得到 %+v", s)
	}
}
