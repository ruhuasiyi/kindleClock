package state

// HolidayKind 是法定节假日的类型。
type HolidayKind int

const (
	HolidayNone          HolidayKind = iota
	HolidayRest                      // 法定放假
	HolidayWorkdayAdjust             // 调休上班
)

// DayInfo 是某一天的附注信息：农历日、节气、法定节假日。
//
// 这三样都是装饰性的 —— 拿不到就不显示，绝不影响时钟本身。
type DayInfo struct {
	SolarTerm      string // "立冬"；无则空
	LunarMonthName string // "九月"；无则空
	LunarDayName   string // "廿九"；无则空
	GanzhiYear     string // "丙午"（天干地支年）；无则空
	HolidayName    string // "国庆节"；非节假日为空
	HolidayKind    HolidayKind
}

// LunarFull 返回完整的农历月日（「九月廿九」）。缺任一段时返回能拼出的部分。
func (d DayInfo) LunarFull() string {
	return d.LunarMonthName + d.LunarDayName
}

// LunarDayOnly 返回只有日名的农历（「廿九」），供空间有限的日历格使用。
func (d DayInfo) LunarDayOnly() string {
	return d.LunarDayName
}

// Line 返回左栏「星期」与「时钟」之间那一行该显示什么。
//
// 节气优先：一个月只有两次，比农历日更值得一眼看到。
// 农历行优先用天干地支年（「丙午年 九月廿二」）；接口没给年时退回
// 「农历 九月廿二」，不能显示成「年 九月廿二」这种半截话。
func (d DayInfo) Line() string {
	if d.SolarTerm != "" {
		return d.SolarTerm
	}
	if full := d.LunarFull(); full != "" {
		if d.GanzhiYear != "" {
			return d.GanzhiYear + "年 " + full
		}
		return "农历 " + full
	}
	return ""
}

// Tag 返回日期行右侧的标签。
//
// 只要 HolidayName 有值就显示它 —— 接口的 legal_holiday_name 有值本身就
// 意味着「这天是法定节假日」，HolidayKind 只是子分类。类型缺失时把已知的
// 名字藏起来没有好处：这是装饰性标签，多显示一个名字不会误导谁。
func (d DayInfo) Tag() string {
	if d.HolidayKind == HolidayWorkdayAdjust {
		if d.HolidayName != "" {
			return d.HolidayName + "调休"
		}
		return "调休上班"
	}
	return d.HolidayName
}

// Battery 是电量状态。
type Battery struct {
	Pct      int  // 0..100
	Charging bool // status 为 Charging
	Known    bool // false 表示读不到，界面不画
}
