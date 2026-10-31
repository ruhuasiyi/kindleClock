// Package state 定义界面要显示的数据模型，以及从原始数据到「这一刻
// 该显示什么」的纯函数变换。
//
// 这里不涉及时间之外的任何外部依赖：没有网络、没有文件、没有设备。
// 与时间相关的函数都显式接收时间入参（date 或 now），因此测试可以覆盖任意时刻。
package state

import "time"

// Weather 是天气的展示用视图。
type Weather struct {
	Text        string    // "晴"
	Temp        int       // 当前温度
	TempMax     int       // 今日最高
	TempMin     int       // 今日最低
	FeelsLike   *int      // 体感，可选
	Humidity    int       // 相对湿度 %
	WindPower   string    // "3级"
	AQI         *int      // 空气质量指数，可选
	AQICategory string    // "优"
	Tomorrow    *Daily    // 明日预报，可选
	FetchedAt   time.Time // 抓取时间
}

// Daily 是单日预报。
type Daily struct {
	Date      string // "2026-10-30"
	TextDay   string // "多云"
	TempMax   int
	TempMin   int
	PrecipPct int // 降水概率 %
}

// Course 是一门课的展示用视图。时间是当天的绝对时间，便于比较。
type Course struct {
	Name      string
	StartText string // "14:15"
	EndText   string // "14:55"
	Start     time.Time
	End       time.Time
	Section   string // "7-8节"，推算不出时为空
	Room      string
	Teacher   string
}

// Timetable 是「今日课程」的展示用视图。
type Timetable struct {
	Week      int
	Semester  string
	Courses   []Course // 已按开始时间升序、已过滤掉已结束的课
	Message   string   // 无课时的说明文字
	FetchedAt time.Time
	Available bool // 数据是否可用；false 时界面显示不可用提示
}

// CourseSpec 是整学期课表里的一门课 —— 还没落到具体某一天。
type CourseSpec struct {
	Name         string
	Teacher      string
	Room         string
	DayOfWeek    int   // 1=周一 … 7=周日
	StartSection int   // 1 起
	EndSection   int   // 含
	Weeks        []int // 这门课在第几周上
}

// Schedule 是整学期的课表加上「今天是第几周」的上下文。
//
// 和 Timetable 的区别：Timetable 是「今天剩余要上的课」的展示视图，
// 而 Schedule 是原始数据 —— 用户翻到任意一天时都要能算出那天的课。
//
// 零值注意：FetchedAt 是周次推算的锚点，为零时 WeekOf 会返回一个天文
// 数字，CoursesForDay 随之静默返回空（表现为「整学期无课」，没有日志）。
// 从缓存或网络构造 Schedule 时必须保证 FetchedAt 被填上。Week 非正表示
// 后端没识别出当前周次，此时 WeekOf 的结果没有意义。
type Schedule struct {
	Week      int
	Semester  string
	MaxWeek   int
	Specs     []CourseSpec
	FetchedAt time.Time
}

// SystemInfo 是设置页「设备信息」那一行要显示的内容。
//
// 放在 Snapshot 里而不是让渲染层自己去读环境：渲染必须是纯函数，
// 读文件/环境变量的活儿归装配层。
type SystemInfo struct {
	ProgramVersion string    // kindleclock 自身的版本号
	Firmware       string    // 设备固件版本
	Week           int       // 当前教学周
	WeatherAt      time.Time // 天气数据的抓取时刻
	TimetableAt    time.Time // 课表数据的抓取时刻

	// Brightness 是 lipc 背光等级（PW3 为 0..BrightnessMax）。
	// -1 表示读不到 —— 界面显示「—」，而不是把 -1 当成一个档位。
	Brightness    int
	BrightnessMax int // 最大档位；<=0 表示未知

	// Refreshing 表示「手动刷新课表」正在进行中，设置页那一行显示「刷新中…」。
	// 由装配层从 deps 带进来；渲染层只读快照，不碰 goroutine。
	Refreshing bool

	// ScheduleAt 是整学期课表最近一次成功抓取/刷新的时刻。
	// 零值表示还没有课表数据，设置页那一行不显示「上次 HH:MM」。
	ScheduleAt time.Time

	// ScheduleRefreshFailed 表示最近一次手动刷新失败了（直到下一次尝试）。
	// 与 Refreshing 分开：失败后要把「失败」留在屏幕上，而不是装作从未发生。
	ScheduleRefreshFailed bool
}

// Snapshot 是某一分钟的完整显示状态。渲染层只依赖它。
type Snapshot struct {
	Now          time.Time
	Weather      *Weather // nil 表示从未成功过
	WeatherFresh bool     // false 表示已过期，界面应标注滞后
	Timetable    Timetable
	// NextCourseIdx 是 Timetable.Courses 中「当前焦点」的下标：
	// 优先指向第一门尚未开始的课；若当前正有一门在上，则指向它；都没有为 -1。
	NextCourseIdx int

	// Day 是显示日期那天的农历/节气/节假日。零值表示没有数据，
	// 渲染层据 Line()/Tag() 返回空串而不画。
	Day DayInfo

	// Calendar 是可见月份的逐日信息，键为 "2006-01-02"。
	// 日历要标出每一天的节假日，而 Day 只有当前那一天 —— 没有整月数据
	// 就只能标今天，那没有意义。由装配层填充；为 nil 时日历不画任何标记。
	Calendar map[string]DayInfo

	// Battery 是电量。Known 为 false 时渲染层不画。
	Battery Battery

	// Info 是设置页「设备信息」的内容。由装配层填充 —— 渲染层不读
	// 文件或环境变量，才能保持纯函数。
	Info SystemInfo
}
