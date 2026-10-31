package state

import (
	"fmt"
	"sort"
	"time"

	"github.com/rhsy/kindleclock/src/internal/config"
)

// WeekOf 算某一天是第几教学周。
//
// 不需要学期起始日：Schedule.Week 是「今天第几周」，以它为锚点，
// 按两天相隔几个「周一」来偏移即可。Schedule.Week 非正时结果无意义
// （后端可能识别不出当前周次）。
//
// date 的墙钟时间按其自身 Location 解释；周次按日历日比较，因此
// date 与 s.FetchedAt 的 UTC 偏移不同也不会算错。为让 Course 的绝对
// 时间与其它显示一致，调用方通常应让 date 与 s.FetchedAt 处于同一 UTC 偏移。
func WeekOf(s *Schedule, date time.Time) int {
	if s == nil {
		return 0
	}
	return s.Week + weeksBetween(mondayOf(s.FetchedAt), mondayOf(date))
}

// weeksBetween 返回两个周一之间隔了多少周。
//
// 按日历日相减，不用绝对时长：7 天在混用时区或夏令时下是 160~169 小时，
// 用 Hours()/168 再做 int 截断会整整差一周，而且早于锚点时因为向零取整
// 反而是对的，这种不对称极难排查。归一到 UTC 的同一时刻只比较日期天数。
func weeksBetween(mondayA, mondayB time.Time) int {
	a := time.Date(mondayA.Year(), mondayA.Month(), mondayA.Day(), 0, 0, 0, 0, time.UTC)
	b := time.Date(mondayB.Year(), mondayB.Month(), mondayB.Day(), 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours()) / (24 * 7)
}

// mondayOf 返回该日期所在周的周一。
func mondayOf(t time.Time) time.Time {
	// time.Weekday 里周日是 0，要挪到末尾
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	d := t.AddDate(0, 0, -(wd - 1))
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
}

// CoursesForDay 返回某一天要显示的课程，按开始时间升序。
//
// 节次到时间的换算依赖 config.Periods；换算不出来的课被跳过（而不是让
// 整天的课都消失）。非教学周返回空。
//
// date 的墙钟时间按其自身 Location 解释，返回的 Course.Start/End 也落在
// 该 Location；调用方应让 date 与 Schedule.FetchedAt 处于同一 UTC 偏移，
// 以保证与其它显示（如 Snapshot.Now）的绝对时间一致。
func CoursesForDay(s *Schedule, date time.Time, periods []config.Period) []Course {
	if s == nil || len(periods) == 0 {
		return nil
	}
	week := WeekOf(s, date)
	if week < 1 || (s.MaxWeek > 0 && week > s.MaxWeek) {
		return nil
	}
	dow := int(date.Weekday())
	if dow == 0 {
		dow = 7
	}

	var out []Course
	for _, spec := range s.Specs {
		if spec.DayOfWeek != dow || !containsInt(spec.Weeks, week) {
			continue
		}
		if spec.StartSection < 1 || spec.EndSection > len(periods) ||
			spec.StartSection > spec.EndSection {
			continue
		}
		start := periods[spec.StartSection-1]
		end := periods[spec.EndSection-1]
		out = append(out, Course{
			Name:      spec.Name,
			Teacher:   spec.Teacher,
			Room:      spec.Room,
			StartText: fmt.Sprintf("%02d:%02d", start.StartMin/60, start.StartMin%60),
			EndText:   fmt.Sprintf("%02d:%02d", end.EndMin/60, end.EndMin%60),
			Start:     atMinutes(date, start.StartMin),
			End:       atMinutes(date, end.EndMin),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func atMinutes(date time.Time, mins int) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(),
		mins/60, mins%60, 0, 0, date.Location())
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
