package state

import "testing"

// 节气优先于农历日：一个月只有两次节气，比农历日更值得一眼看到。
func TestDayInfoLinePrefersSolarTerm(t *testing.T) {
	d := DayInfo{SolarTerm: "立冬", LunarMonthName: "九月", LunarDayName: "廿九"}
	if got := d.Line(); got != "立冬" {
		t.Fatalf("有节气时应显示节气，得到 %q", got)
	}
}

// 有天干地支年时用「丙午年 九月廿一」，不再带「农历」前缀。
func TestDayInfoLineUsesGanzhiYear(t *testing.T) {
	d := DayInfo{LunarMonthName: "九月", LunarDayName: "廿一", GanzhiYear: "丙午"}
	if got := d.Line(); got != "丙午年 九月廿一" {
		t.Fatalf("有干支年时应显示「丙午年 九月廿一」，得到 %q", got)
	}
}

func TestDayInfoLineFallsBackToLunarWithoutGanzhi(t *testing.T) {
	d := DayInfo{LunarMonthName: "九月", LunarDayName: "廿一"}
	if got := d.Line(); got != "农历 九月廿一" {
		t.Fatalf("无干支年时应退回「农历 九月廿一」，得到 %q", got)
	}
}

// 两样都没有时返回空串，渲染层据此不画 —— 这是「接口失败不影响时钟」的落点。
func TestDayInfoLineEmptyWhenNoData(t *testing.T) {
	if got := (DayInfo{}).Line(); got != "" {
		t.Fatalf("无数据时应返回空串，得到 %q", got)
	}
}

func TestLunarFullAndDayOnly(t *testing.T) {
	d := DayInfo{LunarMonthName: "九月", LunarDayName: "廿二"}
	if got := d.LunarFull(); got != "九月廿二" {
		t.Fatalf("LunarFull() = %q，期望「九月廿二」", got)
	}
	if got := d.LunarDayOnly(); got != "廿二" {
		t.Fatalf("LunarDayOnly() = %q，期望「廿二」", got)
	}
}

func TestDayInfoTag(t *testing.T) {
	cases := []struct {
		name string
		in   DayInfo
		want string
	}{
		{"法定放假", DayInfo{HolidayName: "国庆节", HolidayKind: HolidayRest}, "国庆节"},
		{"调休上班带节日名", DayInfo{HolidayName: "春节", HolidayKind: HolidayWorkdayAdjust}, "春节调休"},
		{"调休上班无节日名", DayInfo{HolidayKind: HolidayWorkdayAdjust}, "调休上班"},
		{"普通日子", DayInfo{}, ""},
		// 有名字但类型缺失：照样显示。接口的 legal_holiday_name 有值
		// 本身就意味着「这天是法定节假日」，类型只是子分类，缺了不该把已知信息藏起来。
		{"有名字无类型", DayInfo{HolidayName: "某某节"}, "某某节"},
	}
	for _, c := range cases {
		if got := c.in.Tag(); got != c.want {
			t.Errorf("%s: Tag() = %q，期望 %q", c.name, got, c.want)
		}
	}
}
