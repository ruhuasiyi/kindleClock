package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sampleWeather 是 uapis.cn 的实测响应（2026-10-29 昆明），
// 只保留我们解析的字段。
const sampleWeather = `{
  "province": "云南省", "city": "昆明市", "adcode": "530100",
  "weather": "晴", "weather_icon": "100", "temperature": 23,
  "wind_direction": "南风", "wind_power": "3级", "humidity": 51,
  "report_time": "9 分钟前发布",
  "temp_max": 23, "temp_min": 14,
  "feels_like": 21, "aqi": 28, "aqi_category": "优",
  "forecast": [
    {"date": "2026-10-30", "week": "星期五",
     "temp_max": 24, "temp_min": 13,
     "weather_day": "多云", "weather_night": "多云", "pop": 60}
  ]
}`

func TestWeatherFetchHappyPath(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleWeather))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "昆明", srv.Client())
	now := time.Date(2026, 10, 29, 15, 30, 0, 0, time.UTC)
	w, err := f.Fetch(context.Background(), now)
	if err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	if gotPath != "/api/v1/misc/weather" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "forecast=true") || !strings.Contains(gotQuery, "extended=true") {
		t.Errorf("query = %q，应包含 forecast=true 与 extended=true", gotQuery)
	}
	if w.Text != "晴" {
		t.Errorf("Text = %q", w.Text)
	}
	if w.Temp != 23 || w.TempMax != 23 || w.TempMin != 14 {
		t.Errorf("温度 = %d/%d/%d", w.Temp, w.TempMax, w.TempMin)
	}
	if w.FeelsLike == nil || *w.FeelsLike != 21 {
		t.Errorf("FeelsLike = %v", w.FeelsLike)
	}
	if w.AQI == nil || *w.AQI != 28 || w.AQICategory != "优" {
		t.Errorf("AQI = %v / %q", w.AQI, w.AQICategory)
	}
	if w.Tomorrow == nil {
		t.Fatal("Tomorrow 不应为 nil")
	}
	if w.Tomorrow.TextDay != "多云" || w.Tomorrow.PrecipPct != 60 {
		t.Errorf("Tomorrow = %+v", *w.Tomorrow)
	}
	if !w.FetchedAt.Equal(now) {
		t.Errorf("FetchedAt = %v, 期望 %v", w.FetchedAt, now)
	}
}

func TestWeatherFetchOptionalFieldsMissing(t *testing.T) {
	body := `{"city":"昆明市","weather":"晴","temperature":23,
	  "humidity":51,"wind_power":"3级"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "昆明", srv.Client())
	w, err := f.Fetch(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("缺可选字段不应报错: %v", err)
	}
	if w.FeelsLike != nil {
		t.Error("FeelsLike 应为 nil")
	}
	if w.AQI != nil {
		t.Error("AQI 应为 nil")
	}
	if w.Tomorrow != nil {
		t.Error("Tomorrow 应为 nil")
	}
	if w.Text != "晴" || w.Temp != 23 {
		t.Errorf("必填字段丢失: %+v", w)
	}
}

func TestWeatherFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"code":"SERVICE_UNAVAILABLE"}`))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "昆明", srv.Client())
	_, err := f.Fetch(context.Background(), time.Now())
	if err == nil {
		t.Fatal("503 应报错")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("错误应包含状态码，得到 %v", err)
	}
}

func TestWeatherFetchBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "昆明", srv.Client())
	if _, err := f.Fetch(context.Background(), time.Now()); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestWeatherFetchContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// sleep 后返回合法 JSON：这样「成功返回」不再是错误，
		// 只有真正传播了 ctx 才会失败
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte(`{"weather":"晴","temperature":20}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	f := NewWeatherFetcher(srv.URL, "昆明", srv.Client())
	_, err := f.Fetch(ctx, time.Now())
	if err == nil {
		t.Fatal("ctx 已超时却成功返回 —— 说明 ctx 没有被传播到请求")
	}
	if ctx.Err() == nil {
		t.Error("ctx 应已超时")
	}
}

func TestWeatherSendsCityAndUserAgent(t *testing.T) {
	var gotCity, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCity = r.URL.Query().Get("city")
		gotUA = r.Header.Get("User-Agent")
		w.Write([]byte(`{"weather":"晴","temperature":20}`))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "昆明", srv.Client())
	if _, err := f.Fetch(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if gotCity != "昆明" {
		t.Errorf("city 查询参数 = %q，期望 昆明", gotCity)
	}
	// 必须断言具体值：不设头时 Go 的 http.Client 会自动补
	// "Go-http-client/1.1"，只判空杀不掉「删掉 UA 头」的变异。
	if gotUA != "kindleclock/0.1" {
		t.Errorf("User-Agent = %q，期望 kindleclock/0.1", gotUA)
	}
}

func TestWeatherMapsTempMaxFromForecastField(t *testing.T) {
	// temp_max/temp_min 必须来自同名 JSON 字段，不能误读 temperature
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"weather":"晴","temperature":20,"temp_max":31,"temp_min":9}`))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "x", srv.Client())
	w, err := f.Fetch(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w.Temp != 20 || w.TempMax != 31 || w.TempMin != 9 {
		t.Errorf("温度映射错误: Temp=%d TempMax=%d TempMin=%d，期望 20/31/9",
			w.Temp, w.TempMax, w.TempMin)
	}
}

func TestWeatherTomorrowFieldsPopulated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"weather":"晴","temperature":20,
		  "forecast":[{"date":"2026-10-30","temp_max":24,"temp_min":13,
		               "weather_day":"多云","pop":60}]}`))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "x", srv.Client())
	w, err := f.Fetch(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w.Tomorrow == nil {
		t.Fatal("Tomorrow 不应为 nil")
	}
	if w.Tomorrow.Date != "2026-10-30" {
		t.Errorf("Tomorrow.Date = %q", w.Tomorrow.Date)
	}
	if w.Tomorrow.TempMax != 24 || w.Tomorrow.TempMin != 13 {
		t.Errorf("Tomorrow 温度 = %d/%d", w.Tomorrow.TempMax, w.Tomorrow.TempMin)
	}
}

func TestWeatherBodySizeLimit(t *testing.T) {
	// 超过 1 MiB 的响应必须被截断（截断后 JSON 解析失败即证明限长生效）
	big := `{"weather":"` + strings.Repeat("x", 2<<20) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(big))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "x", srv.Client())
	_, err := f.Fetch(context.Background(), time.Now())
	if err == nil {
		t.Fatal("超过 1 MiB 的响应应因截断而解析失败")
	}
}

func TestWeatherAQICategoryEmptyWhenMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只有 aqi_category，没有 aqi
		w.Write([]byte(`{"weather":"晴","temperature":20,"aqi_category":"优"}`))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL, "x", srv.Client())
	w, err := f.Fetch(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w.AQI != nil {
		t.Error("AQI 应为 nil")
	}
	if w.AQICategory != "" {
		t.Errorf("AQI 缺失时 AQICategory 应留空，得到 %q", w.AQICategory)
	}
}

func TestWeatherBaseURLTrailingSlash(t *testing.T) {
	// baseURL 带尾斜杠时不能拼出 //api/v1/...
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"weather":"晴","temperature":20}`))
	}))
	defer srv.Close()

	f := NewWeatherFetcher(srv.URL+"/", "昆明", srv.Client())
	if _, err := f.Fetch(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/misc/weather" {
		t.Errorf("path = %q，期望 /api/v1/misc/weather", gotPath)
	}
}
