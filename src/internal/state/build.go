package state

import (
	"sort"
	"time"

	"github.com/rhsy/kindleclock/src/internal/config"
)

// Build 把原始数据裁剪成「这一刻该显示什么」。
//
// 规则：
//   - 课程只保留 结束时间 > now 的（正在上的课保留）
//   - 剩余课程按开始时间升序排列，NextCourseIdx 是排序后的下标
//   - NextCourseIdx 优先指向第一门尚未开始的课；若当前正有一门在上且之后
//     没有别的课，则回退指向那门正在上的课；都没有则为 -1
//   - WeatherFresh = 抓取时间距今 <= weatherStale
//   - 课表不使用陈旧阈值：spec §8 规定课表拉取失败只记日志、界面不报错，
//     由 Timetable.Available 承载「能不能显示」
//   - 不修改入参，Courses 一定是新的切片
func Build(now time.Time, w *Weather, tt Timetable, periods []config.Period,
	weatherStale time.Duration) Snapshot {

	snap := Snapshot{
		Now:           now,
		Weather:       w,
		NextCourseIdx: -1,
	}

	if w != nil {
		snap.WeatherFresh = now.Sub(w.FetchedAt) <= weatherStale
	}

	// 第一趟：复制并过滤课程，绝不写回 tt.Courses 的底层数组
	out := tt
	out.Courses = make([]Course, 0, len(tt.Courses))
	for _, c := range tt.Courses {
		if !c.End.After(now) {
			continue // 已结束（含恰好结束于 now）
		}
		if c.Section == "" {
			c.Section = config.LabelSection(periods, c.StartText, c.EndText)
		}
		out.Courses = append(out.Courses, c)
	}

	// 第二趟：排序。上游的课程顺序不受信任，NextCourseIdx 必须按时间定位
	sort.SliceStable(out.Courses, func(i, j int) bool {
		return out.Courses[i].Start.Before(out.Courses[j].Start)
	})

	// 第三趟：在排序后的切片里定位焦点
	if tt.Available {
		inProgressIdx := -1
		for i, c := range out.Courses {
			if c.Start.After(now) {
				snap.NextCourseIdx = i
				break
			}
			if inProgressIdx < 0 {
				inProgressIdx = i // 排序后第一门已开始且未结束的课
			}
		}
		if snap.NextCourseIdx < 0 {
			snap.NextCourseIdx = inProgressIdx
		}
	} else {
		// 数据不可用时不给出剩余课程（哪怕还带着上次成功的列表）
		out.Courses = nil
	}

	snap.Timetable = out

	return snap
}
