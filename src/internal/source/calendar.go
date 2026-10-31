package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rhsy/kindleclock/src/internal/state"
)

// CalendarFetcher 抓 uapis 的节假日万年历。
//
// 一个接口同时给出农历日、节气、法定节假日与调休。香港天文台没有节气的
// JSON 接口（opendata 下只有 lunardate.php，不含节气；试过 solarterm.php
// 等路径全部 404），所以这里不去拼第二个数据源 —— 多一个源只是多一种失败模式。
type CalendarFetcher struct {
	baseURL string
	client  *http.Client
}

// NewCalendarFetcher 构造。client 为 nil 时用带超时的默认客户端。
//
// 必须有超时：抓取协程是同步等这一趟返回的，连接挂住会让这条抓取线
// 永久停摆，而且没有任何日志。
func NewCalendarFetcher(baseURL string, client *http.Client) *CalendarFetcher {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &CalendarFetcher{baseURL: baseURL, client: client}
}

type calendarResp struct {
	Days []struct {
		Date             string `json:"date"`
		SolarTerm        string `json:"solar_term"`
		LunarMonthName   string `json:"lunar_month_name"`
		LunarDayName     string `json:"lunar_day_name"`
		GanzhiYear       string `json:"ganzhi_year"`
		LegalHolidayName string `json:"legal_holiday_name"`
		LegalHolidayType string `json:"legal_holiday_type"`
	} `json:"days"`
}

// FetchYear 抓某一年的逐日信息，键是 "2006-01-02" 格式的日期。
//
// 返回 0 天视为错误：不能让人拿一张空表覆盖掉缓存里的好数据。
func (f *CalendarFetcher) FetchYear(ctx context.Context, year int) (map[string]state.DayInfo, error) {
	url := strings.TrimRight(f.baseURL, "/") + "/api/v1/misc/holiday-calendar?year=" + strconv.Itoa(year)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造日历请求: %w", err)
	}
	req.Header.Set("User-Agent", "kindleclock/0.1")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求日历: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("读日历响应: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("日历返回 %d: %.200q", resp.StatusCode, body)
	}

	var r calendarResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析日历 JSON: %w", err)
	}
	if len(r.Days) == 0 {
		return nil, fmt.Errorf("日历返回了 0 天（%d 年）", year)
	}

	out := make(map[string]state.DayInfo, len(r.Days))
	for _, d := range r.Days {
		if d.Date == "" {
			continue // 没有日期就没法索引，跳过而不是写进空串键
		}
		info := state.DayInfo{
			SolarTerm: d.SolarTerm,
			// 接口把农历拆成月名与日名两段；保留两段，显示时按场景拼。
			// 旧版本把两段拼成一个 LunarDay 字段，缓存格式因此变了 ——
			// 部署时要删掉旧的 calendar-*.json 让它重抓。
			LunarMonthName: d.LunarMonthName,
			LunarDayName:   d.LunarDayName,
			GanzhiYear:     d.GanzhiYear,
			HolidayName:    d.LegalHolidayName,
		}
		switch d.LegalHolidayType {
		case "rest":
			info.HolidayKind = state.HolidayRest
		case "workday_adjust":
			info.HolidayKind = state.HolidayWorkdayAdjust
		}
		out[d.Date] = info
	}
	// 循环里会跳过空 date；万一上游改了字段名，全部记录都会被跳过，
	// 此时返回空表会覆盖缓存里的好数据 —— 按错误处理。
	if len(out) == 0 {
		return nil, fmt.Errorf("日历里没有一条带有效日期的记录（%d 年）", year)
	}
	return out, nil
}
