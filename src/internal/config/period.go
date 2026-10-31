package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Period 表示一个节次，单位为「距午夜的分钟数」。
// 用整数而不是 time.Time：节次表是每天重复的模板，没有日期概念。
type Period struct {
	StartMin int
	EndMin   int
}

// parsePeriods 解析 ["08:20-09:00", ...]，并校验严格升序、不重叠。
func parsePeriods(times []string) ([]Period, error) {
	out := make([]Period, 0, len(times))
	for i, s := range times {
		start, end, err := parsePeriod(s)
		if err != nil {
			return nil, fmt.Errorf("periods.times[%d] %q: %w", i, s, err)
		}
		if len(out) > 0 && start < out[len(out)-1].EndMin {
			return nil, fmt.Errorf("periods.times 必须按时间升序且不重叠：第 %d 项 %q 与前一项冲突", i, s)
		}
		out = append(out, Period{StartMin: start, EndMin: end})
	}
	return out, nil
}

// parsePeriod 解析 "HH:MM-HH:MM"。
func parsePeriod(s string) (start, end int, err error) {
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("格式应为 HH:MM-HH:MM")
	}
	start, err = parseHM(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("格式应为 HH:MM-HH:MM: %w", err)
	}
	end, err = parseHM(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("格式应为 HH:MM-HH:MM: %w", err)
	}
	if end <= start {
		return 0, 0, fmt.Errorf("结束时间必须晚于开始时间")
	}
	return start, end, nil
}

// parseHM 解析 "HH:MM"，要求两位数小时与分钟。
func parseHM(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("时间格式应为 HH:MM，实际 %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("小时非法: %q", parts[0])
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("分钟非法: %q", parts[1])
	}
	return h*60 + m, nil
}

// LabelSection 由 "HH:MM" 起止时间推算「第几节」。
//
// 规则：开始时间精确等于第 N 节的开始、结束时间精确等于第 M 节的结束，
// 则返回 "N-M节"（N==M 时返回 "N节"）。任一不匹配返回空串 —— 宁可
// 不显示节次，也不显示错的。
func LabelSection(periods []Period, startText, endText string) string {
	if len(periods) == 0 {
		return ""
	}
	startMin, err := parseHM(startText)
	if err != nil {
		return ""
	}
	endMin, err := parseHM(endText)
	if err != nil {
		return ""
	}
	n, m := -1, -1
	for i, p := range periods {
		if p.StartMin == startMin {
			n = i + 1
		}
		if p.EndMin == endMin {
			m = i + 1
		}
	}
	if n < 0 || m < 0 || m < n {
		return ""
	}
	if n == m {
		return fmt.Sprintf("%d节", n)
	}
	return fmt.Sprintf("%d-%d节", n, m)
}
