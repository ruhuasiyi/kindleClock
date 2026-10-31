package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rhsy/kindleclock/src/internal/state"
)

// 真机实测的真实响应片段（2026 年）：节气、农历、法定放假、调休上班都在里面。
const calendarFixture = `{
  "mode": "year",
  "days": [
    {"date":"2026-11-07","lunar_month_name":"九月","lunar_day_name":"廿九","ganzhi_year":"丙午","solar_term":"立冬"},
    {"date":"2026-10-30","lunar_month_name":"九月","lunar_day_name":"廿一","ganzhi_year":"丙午"},
    {"date":"2026-10-01","lunar_month_name":"八月","lunar_day_name":"廿一","ganzhi_year":"丙午","legal_holiday_name":"国庆节","legal_holiday_type":"rest"},
    {"date":"2026-10-10","lunar_month_name":"八月","lunar_day_name":"三十","ganzhi_year":"丙午","legal_holiday_name":"国庆节","legal_holiday_type":"workday_adjust"}
  ]
}`

func newCalendarServer(t *testing.T, body string, status int) (*httptest.Server, *string) {
	t.Helper()
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotURL
}

func TestFetchCalendarParsesFields(t *testing.T) {
	srv, gotURL := newCalendarServer(t, calendarFixture, http.StatusOK)
	f := NewCalendarFetcher(srv.URL, srv.Client())

	got, err := f.FetchYear(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if *gotURL != "/api/v1/misc/holiday-calendar?year=2026" {
		t.Fatalf("请求路径不对: %s", *gotURL)
	}
	if len(got) != 4 {
		t.Fatalf("应解析出 4 天，得到 %d", len(got))
	}

	term := got["2026-11-07"]
	if term.SolarTerm != "立冬" || term.LunarMonthName != "九月" || term.LunarDayName != "廿九" {
		t.Fatalf("节气日解析错: %+v", term)
	}
	if term.GanzhiYear != "丙午" {
		t.Fatalf("节气日的干支年 = %q，期望「丙午」", term.GanzhiYear)
	}
	if term.LunarFull() != "九月廿九" || term.LunarDayOnly() != "廿九" {
		t.Fatalf("农历拆分/拼接错: full=%q day=%q", term.LunarFull(), term.LunarDayOnly())
	}
	if term.HolidayKind != state.HolidayNone {
		t.Fatalf("节气日不该有节假日类型: %+v", term)
	}

	plain := got["2026-10-30"]
	if plain.SolarTerm != "" || plain.LunarFull() != "九月廿一" {
		t.Fatalf("普通日解析错: %+v", plain)
	}

	rest := got["2026-10-01"]
	if rest.HolidayKind != state.HolidayRest || rest.HolidayName != "国庆节" {
		t.Fatalf("法定放假解析错: %+v", rest)
	}

	adj := got["2026-10-10"]
	if adj.HolidayKind != state.HolidayWorkdayAdjust {
		t.Fatalf("调休上班解析错: %+v", adj)
	}
}

// 字段缺失是常态（普通日子既没节气也没节假日），不能因此报错。
func TestFetchCalendarToleratesMissingFields(t *testing.T) {
	srv, _ := newCalendarServer(t,
		`{"days":[{"date":"2026-03-15"}]}`, http.StatusOK)
	f := NewCalendarFetcher(srv.URL, srv.Client())

	got, err := f.FetchYear(context.Background(), 2026)
	if err != nil {
		t.Fatalf("字段全缺不该报错: %v", err)
	}
	if d, ok := got["2026-03-15"]; !ok {
		t.Fatal("这一天应该存在")
	} else if d.LunarFull() != "" || d.SolarTerm != "" || d.GanzhiYear != "" {
		t.Fatalf("缺字段应留空，得到 %+v", d)
	}
}

// 上游若把 date 字段改名，循环会跳过所有记录；此时不能返回空表 ——
// 空表会经 SaveCalendar 覆盖缓存里的好数据。必须按错误处理。
//
// 注意不能把键写成 "Date"：encoding/json 对键名大小写不敏感，
// "Date" 仍会匹配 `json:"date"`，测不到这条护栏。用真正不同的键名。
func TestFetchCalendarRejectsAllEmptyDates(t *testing.T) {
	srv, _ := newCalendarServer(t,
		`{"days":[{"day":"2026-03-15"}]}`, http.StatusOK)
	f := NewCalendarFetcher(srv.URL, srv.Client())

	got, err := f.FetchYear(context.Background(), 2026)
	if err == nil {
		t.Fatalf("所有记录都没有有效日期时应报错，得到空表 %+v", got)
	}
	if got != nil {
		t.Fatalf("报错时不应返回任何数据，得到 %+v", got)
	}
}

func TestFetchCalendarRejectsBadResponses(t *testing.T) {
	t.Run("非 200", func(t *testing.T) {
		srv, _ := newCalendarServer(t, `{"code":400,"message":"bad"}`, http.StatusBadRequest)
		f := NewCalendarFetcher(srv.URL, srv.Client())
		if _, err := f.FetchYear(context.Background(), 2026); err == nil {
			t.Fatal("400 应该报错")
		}
	})
	t.Run("不是 JSON", func(t *testing.T) {
		srv, _ := newCalendarServer(t, `Not Available`, http.StatusOK)
		f := NewCalendarFetcher(srv.URL, srv.Client())
		if _, err := f.FetchYear(context.Background(), 2026); err == nil {
			t.Fatal("非 JSON 应该报错")
		}
	})
	t.Run("0 天", func(t *testing.T) {
		srv, _ := newCalendarServer(t, `{"days":[]}`, http.StatusOK)
		f := NewCalendarFetcher(srv.URL, srv.Client())
		if _, err := f.FetchYear(context.Background(), 2026); err == nil {
			t.Fatal("0 天应该报错 —— 否则缓存会被写成空表覆盖掉好数据")
		}
	})
}

// 连接挂住时必须在超时后报错返回，而不是永久阻塞。
// 抓取协程是同步等这一趟的，卡住就等于这条抓取线从此刻起彻底停摆。
func TestFetchCalendarTimesOutInsteadOfHanging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // 永不主动返回
	}))
	t.Cleanup(srv.Close)

	f := NewCalendarFetcher(srv.URL, &http.Client{Timeout: 100 * time.Millisecond})

	done := make(chan error, 1)
	go func() { _, err := f.FetchYear(context.Background(), 2026); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("超时应该返回错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("FetchYear 挂住了 —— 客户端没有超时")
	}
}

// 默认客户端必须有超时：抓取协程同步等这一趟，连接挂住会永久停摆。
func TestNewCalendarFetcherDefaultClientHasTimeout(t *testing.T) {
	f := NewCalendarFetcher("http://example.invalid", nil)
	if f.client.Timeout != 20*time.Second {
		t.Fatalf("默认 client 超时 = %v，期望 20s", f.client.Timeout)
	}
}

// baseURL 带尾斜杠时不能拼出 "//api/..." —— 那会静默 404，
// 表现是日历永远拿不到数据，但时钟照常走，极难发现。
func TestFetchCalendarBaseURLTrailingSlash(t *testing.T) {
	srv, gotURL := newCalendarServer(t, calendarFixture, http.StatusOK)
	f := NewCalendarFetcher(srv.URL+"/", srv.Client())

	if _, err := f.FetchYear(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	if *gotURL != "/api/v1/misc/holiday-calendar?year=2026" {
		t.Fatalf("尾斜杠没被处理，请求路径变成了 %s", *gotURL)
	}
}

// 按年分文件：两年的缓存必须互不覆盖。
func TestCalendarCachePerYearFiles(t *testing.T) {
	st := NewStore(t.TempDir())
	c2025 := map[string]state.DayInfo{
		"2025-01-01": {SolarTerm: "元旦"},
	}
	c2026 := map[string]state.DayInfo{
		"2026-10-01": {HolidayName: "国庆节", HolidayKind: state.HolidayRest},
	}
	if err := st.SaveCalendar(2025, c2025); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCalendar(2026, c2026); err != nil {
		t.Fatal(err)
	}

	got2025, err := st.LoadCalendar(2025)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2025) != 1 || got2025["2025-01-01"] != c2025["2025-01-01"] {
		t.Fatalf("2025 缓存被覆盖或内容不对: %+v", got2025)
	}
	got2026, err := st.LoadCalendar(2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2026) != 1 || got2026["2026-10-01"] != c2026["2026-10-01"] {
		t.Fatalf("2026 缓存内容不对: %+v", got2026)
	}
}

func TestCalendarCacheRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	want := map[string]state.DayInfo{
		"2026-11-07": {SolarTerm: "立冬", LunarMonthName: "九月", LunarDayName: "廿九", GanzhiYear: "丙午"},
		"2026-10-01": {HolidayName: "国庆节", HolidayKind: state.HolidayRest},
	}
	if err := st.SaveCalendar(2026, want); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadCalendar(2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("条数不对: %d vs %d", len(got), len(want))
	}
	if got["2026-11-07"] != want["2026-11-07"] {
		t.Fatalf("内容不对: %+v", got["2026-11-07"])
	}
	if got["2026-10-01"] != want["2026-10-01"] {
		t.Fatalf("内容不对: %+v", got["2026-10-01"])
	}
}
