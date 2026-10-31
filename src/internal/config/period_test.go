package config

import "testing"

func mkPeriods() []Period {
	return []Period{
		{8*60 + 20, 9 * 60},
		{9*60 + 5, 9*60 + 45},
		{10 * 60, 10*60 + 40},
		{10*60 + 45, 11*60 + 25},
	}
}

func TestLabelSectionExactMatch(t *testing.T) {
	cases := []struct {
		start, end, want string
	}{
		{"08:20", "09:45", "1-2节"},
		{"10:00", "10:40", "3节"},
		{"10:00", "11:25", "3-4节"},
	}
	for _, c := range cases {
		got := LabelSection(mkPeriods(), c.start, c.end)
		if got != c.want {
			t.Errorf("LabelSection(%q,%q) = %q, 期望 %q", c.start, c.end, got, c.want)
		}
	}
}

func TestLabelSectionNoMatch(t *testing.T) {
	if got := LabelSection(mkPeriods(), "08:25", "09:45"); got != "" {
		t.Errorf("开始时间不匹配应返回空，得到 %q", got)
	}
	if got := LabelSection(mkPeriods(), "08:20", "09:50"); got != "" {
		t.Errorf("结束时间不匹配应返回空，得到 %q", got)
	}
	// 结束节次早于开始节次，属于自相矛盾的区间，不能拼出 "2-1节"
	if got := LabelSection(mkPeriods(), "09:05", "09:00"); got != "" {
		t.Errorf("结束节次早于开始节次应返回空，得到 %q", got)
	}
}

func TestLabelSectionEmptyPeriods(t *testing.T) {
	if got := LabelSection(nil, "08:20", "09:45"); got != "" {
		t.Errorf("节次表为空应返回空，得到 %q", got)
	}
}

func TestParsePeriodRejectsReversed(t *testing.T) {
	if _, _, err := parsePeriod("09:00-08:20"); err == nil {
		t.Fatal("结束早于开始应报错")
	}
}
