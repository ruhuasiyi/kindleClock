// Package source 负责从外部获取数据，并转成 state 包的展示模型。
//
// 这里负责取数与模型转换；磁盘缓存见 store.go，重试调度属于主循环。
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rhsy/kindleclock/src/internal/state"
)

// maxBodyBytes 是响应体读取上限（1 MiB），防止异常响应把内存撑爆。
const maxBodyBytes = 1 << 20

// WeatherFetcher 拉取 uapis.cn 的天气数据。
type WeatherFetcher struct {
	baseURL string
	city    string
	client  *http.Client
}

// NewWeatherFetcher 构造。client 为 nil 时用带超时的默认客户端。
func NewWeatherFetcher(baseURL, city string, client *http.Client) *WeatherFetcher {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &WeatherFetcher{baseURL: baseURL, city: city, client: client}
}

// uapisResp 是 uapis.cn 的原始响应。所有字段都可能缺失。
type uapisResp struct {
	Weather     string   `json:"weather"`
	Temperature *float64 `json:"temperature"`
	TempMax     *float64 `json:"temp_max"`
	TempMin     *float64 `json:"temp_min"`
	Humidity    *float64 `json:"humidity"`
	WindPower   string   `json:"wind_power"`
	FeelsLike   *float64 `json:"feels_like"`
	AQI         *float64 `json:"aqi"`
	AQICategory string   `json:"aqi_category"`
	Forecast    []struct {
		Date       string   `json:"date"`
		TempMax    *float64 `json:"temp_max"`
		TempMin    *float64 `json:"temp_min"`
		WeatherDay string   `json:"weather_day"`
		Pop        *float64 `json:"pop"`
	} `json:"forecast"`
}

// Fetch 拉一次天气。now 由调用方传入，用于打 FetchedAt 时间戳。
func (f *WeatherFetcher) Fetch(ctx context.Context, now time.Time) (*state.Weather, error) {
	u, err := url.Parse(strings.TrimRight(f.baseURL, "/") + "/api/v1/misc/weather")
	if err != nil {
		return nil, fmt.Errorf("构造 URL: %w", err)
	}
	q := u.Query()
	q.Set("city", f.city)
	q.Set("forecast", "true")
	q.Set("extended", "true")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kindleclock/0.1")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求天气: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("读响应: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("天气接口返回 %d: %.200q", resp.StatusCode, body)
	}

	var raw uapisResp
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("解析天气 JSON: %w", err)
	}

	w := &state.Weather{
		Text:      raw.Weather,
		Humidity:  roundPtr(raw.Humidity),
		WindPower: raw.WindPower,
		FetchedAt: now,
	}
	// uapis 的顶层 temperature 是当前温度；temp_max/min 需要 forecast=true
	if raw.Temperature != nil {
		w.Temp = roundPtr(raw.Temperature)
	}
	if raw.TempMax != nil {
		w.TempMax = roundPtr(raw.TempMax)
	}
	if raw.TempMin != nil {
		w.TempMin = roundPtr(raw.TempMin)
	}
	if raw.FeelsLike != nil {
		v := roundPtr(raw.FeelsLike)
		w.FeelsLike = &v
	}
	// aqi 缺失时 AQICategory 也留空，界面据此判断是否显示空气质量
	if raw.AQI != nil {
		v := roundPtr(raw.AQI)
		w.AQI = &v
		w.AQICategory = raw.AQICategory
	}
	if len(raw.Forecast) > 0 {
		d := raw.Forecast[0]
		day := &state.Daily{
			Date:    d.Date,
			TextDay: d.WeatherDay,
		}
		if d.TempMax != nil {
			day.TempMax = roundPtr(d.TempMax)
		}
		if d.TempMin != nil {
			day.TempMin = roundPtr(d.TempMin)
		}
		if d.Pop != nil {
			day.PrecipPct = roundPtr(d.Pop)
		}
		// 注意：uapis 的 forecast[0] 是「被查城市的当天」，不是明天。
		// 这里沿用了 state.Weather.Tomorrow 这个历史命名，渲染层不得
		// 直接按字段名当「明日预报」显示（会显示成今天，且高低温与今日重复，
		// 看起来还很合理，极难发现）。若日后要显示明日，必须按
		// forecast[i].Date != 今天 过滤。
		w.Tomorrow = day
	}
	return w, nil
}

// roundPtr 把可选的浮点温度四舍五入成整数；nil 视为 0。
// Go 的 int 转换向零截断，所以正负分别加减 0.5。
func roundPtr(p *float64) int {
	if p == nil {
		return 0
	}
	v := *p
	if v >= 0 {
		return int(v + 0.5)
	}
	return int(v - 0.5)
}
