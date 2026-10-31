// Package ui 保存触摸交互产生的界面状态。
//
// 这些状态不影响「这一刻该显示什么数据」，只影响「显示哪一天、第几页、
// 是否在设置页」。把它们和 state.Snapshot 分开，是为了让渲染层能保持
// 纯函数性质：同样的 Snapshot + State 必须产出同样的图。
package ui

import "time"

// State 是交互状态。
//
// SelectedDate 用零值表示「跟随今天」，这是整个设计的支点：
// 跨零点不用特殊处理（第二天它自动就是新的今天），60 秒回位退化成清零，
// 用户从未交互过时行为与没有这套机制时完全一致。
type State struct {
	SelectedDate time.Time
	MonthOffset  int
	CourseScroll int
	SettingsOpen bool
	LastInput    time.Time
}

// MonthStart 返回日历当前显示月份的 1 日。
//
// 必须先归一到 1 日再做月份运算：直接对「今天」AddDate(0, n, 0) 在 29/30/31 日
// 会跳到错误月份（10-31 加一个月是 12-01，跳过了 11 月）。
func MonthStart(today time.Time, offset int) time.Time {
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	return first.AddDate(0, offset, 0)
}

// FollowToday 报告当前是否处于「跟随今天」的默认态。
func (s State) FollowToday() bool { return s.SelectedDate.IsZero() }

// IsDefault 报告是否所有交互状态都是默认值。
func (s State) IsDefault() bool {
	return s.FollowToday() && s.MonthOffset == 0 &&
		s.CourseScroll == 0 && !s.SettingsOpen
}

// Reset 回到默认态。
func (s *State) Reset() {
	*s = State{}
}

// ResetIfIdle 在超过 idle 没有交互时回位，并报告是否发生了回位。
//
// 不回位的后果很具体：用户翻到 11 月看了一会儿走开，第二天早上看到的
// 还是 11 月的日历和 11 月 2 日的课 —— 而那天已经过去了。
//
// 返回值是给主循环用的：回位会改变整屏内容（月份、选中高亮、设置页），
// 调用方据此强制全屏刷新，局部刷新会留下大片残影。
func (s *State) ResetIfIdle(now time.Time, idle time.Duration) bool {
	if s.IsDefault() || s.LastInput.IsZero() {
		return false
	}
	if now.Sub(s.LastInput) >= idle {
		s.Reset()
		return true
	}
	return false
}

// SelectDate 选中某一天。today 用来判断是否回到「跟随今天」。
func (s *State) SelectDate(date, today time.Time) {
	if sameDay(date, today) {
		s.SelectedDate = time.Time{}
		s.MonthOffset = 0
	} else {
		s.SelectedDate = date
		s.MonthOffset = monthsBetween(today, date)
	}
	s.CourseScroll = 0
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// monthsBetween 返回 from 到 to 相差几个月（只看年月）。
func monthsBetween(from, to time.Time) int {
	return (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
}
