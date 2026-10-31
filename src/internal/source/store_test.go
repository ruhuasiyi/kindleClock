package source

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rhsy/kindleclock/src/internal/state"
)

func TestStoreWeatherRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if _, err := s.LoadWeather(); err == nil {
		t.Error("空目录 LoadWeather 应报错，不能返回零值")
	}

	feels := 21
	aqi := 28
	w := &state.Weather{
		Text: "晴", Temp: 23, TempMax: 23, TempMin: 14,
		FeelsLike: &feels, Humidity: 51, WindPower: "3级",
		AQI: &aqi, AQICategory: "优",
		Tomorrow:  &state.Daily{Date: "2026-10-30", TextDay: "多云", TempMax: 24, TempMin: 13, PrecipPct: 60},
		FetchedAt: time.Date(2026, 10, 29, 15, 30, 0, 0, time.UTC),
	}
	if err := s.SaveWeather(w); err != nil {
		t.Fatalf("SaveWeather: %v", err)
	}

	got, err := s.LoadWeather()
	if err != nil {
		t.Fatalf("LoadWeather: %v", err)
	}
	if got.Text != "晴" || got.Temp != 23 {
		t.Errorf("got %+v", got)
	}
	if got.FeelsLike == nil || *got.FeelsLike != 21 {
		t.Errorf("FeelsLike 丢失")
	}
	if got.Tomorrow == nil || got.Tomorrow.PrecipPct != 60 {
		t.Errorf("Tomorrow 丢失: %+v", got.Tomorrow)
	}
	if !got.FetchedAt.Equal(w.FetchedAt) {
		t.Errorf("FetchedAt = %v", got.FetchedAt)
	}
}

func TestStoreTimetableRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	loc := time.FixedZone("CST", 8*3600)
	tt := &state.Timetable{
		Week: 18, Semester: "2025-2026 第二学期",
		Available: true,
		FetchedAt: time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC),
		Courses: []state.Course{{
			Name: "高等数学", StartText: "08:20", EndText: "09:45",
			Start: time.Date(2026, 10, 29, 8, 20, 0, 0, loc),
			End:   time.Date(2026, 10, 29, 9, 45, 0, 0, loc),
			Room:  "A301",
		}},
	}
	if err := s.SaveTimetable(tt); err != nil {
		t.Fatalf("SaveTimetable: %v", err)
	}
	got, err := s.LoadTimetable()
	if err != nil {
		t.Fatalf("LoadTimetable: %v", err)
	}
	if got.Week != 18 || len(got.Courses) != 1 {
		t.Fatalf("got %+v", got)
	}
	if !got.Courses[0].Start.Equal(tt.Courses[0].Start) {
		t.Errorf("Start 不一致: %v vs %v", got.Courses[0].Start, tt.Courses[0].Start)
	}
}

func TestStoreTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if tok, err := s.LoadToken(); err != nil || tok != "" {
		t.Errorf("无 token 时应返回空串与 nil，得到 %q / %v", tok, err)
	}
	if err := s.SaveToken("abc123"); err != nil {
		t.Fatal(err)
	}
	tok, err := s.LoadToken()
	if err != nil || tok != "abc123" {
		t.Errorf("got %q / %v", tok, err)
	}
}

// 整份替换的验证见下面两个用例：
//   - TestStoreReplacesFileNotInPlace 锁住「rename 换 inode」而非原地覆盖
//   - TestStoreReaderNeverSeesPartialFile 锁住并发读方永远看到完整文件
//
// 本用例只验证覆盖写入后内容正确、且不留 .tmp 残留。
func TestStoreSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if err := s.SaveWeather(&state.Weather{Text: "晴", Temp: 18}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadWeather()
	if err != nil || got.Text != "晴" {
		t.Fatalf("第一次写入后读回失败: %v %+v", err, got)
	}

	// 覆盖为更长的内容（长度变化最容易暴露非原子写）
	if err := s.SaveWeather(&state.Weather{Text: "多云转小雨", Temp: -3}); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadWeather()
	if err != nil {
		t.Fatalf("覆盖写入后读回失败（文件可能已损坏）: %v", err)
	}
	if got.Text != "多云转小雨" || got.Temp != -3 {
		t.Errorf("覆盖后内容不对: %+v", got)
	}

	// 不能残留 .tmp
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

// 目标文件必须是被整份替换的（inode 变化）。
// 直接 WriteFile 覆盖会复用同一 inode，断在写入中间就是半截文件。
func TestStoreReplacesFileNotInPlace(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.SaveWeather(&state.Weather{Text: "a"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "weather.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWeather(&state.Weather{Text: "b"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(dir, "weather.json"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("同一 inode：这是原地覆盖，不是 rename 替换")
	}
}

// 并发读方永远看不到「缺失」或「半截」的文件。
// 注意必须先写好一次初始值 —— 否则会从「文件本来不存在」开始，
// 正确的实现也会因为「第一次读发生在第一次写之前」而失败。
func TestStoreReaderNeverSeesPartialFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.SaveWeather(&state.Weather{Text: "warm"}); err != nil {
		t.Fatal(err)
	}

	big := strings.Repeat("x", 200_000)
	var stop atomic.Bool
	var bad atomic.Int64
	var first atomic.Value
	done := make(chan struct{})

	go func() {
		defer close(done)
		for !stop.Load() {
			if _, err := s.LoadWeather(); err != nil {
				bad.Add(1)
				if first.Load() == nil {
					first.Store(err.Error())
				}
			}
		}
	}()

	for i := 0; i < 300; i++ {
		txt := "short"
		if i%2 == 0 {
			txt = big
		}
		if err := s.SaveWeather(&state.Weather{Text: txt}); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	<-done

	if n := bad.Load(); n > 0 {
		t.Fatalf("读方看到 %d 次不可解析的文件，首个错误: %v", n, first.Load())
	}
}

// 写入失败路径必须清掉 .tmp，不能留残留
func TestStoreCleansTmpOnFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// 让 .tmp 路径成为一个目录 → OpenFile 必然失败（EISDIR），
	// 任何 uid 都能确定性触发，不依赖 root 权限
	tmpPath := filepath.Join(dir, "token.json.tmp")
	if err := os.Mkdir(tmpPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveToken("x"); err == nil {
		t.Fatal("写入应失败")
	}
	// .tmp 是目录，不该被删；但也不能出现「半个文件残留」之外的东西
	if fi, err := os.Stat(tmpPath); err != nil || !fi.IsDir() {
		t.Errorf("失败路径不应动到已有的 .tmp 路径: %v", err)
	}
	// 关键断言：目标文件不存在（没有被半途创建）
	if _, err := os.Stat(filepath.Join(dir, "token.json")); err == nil {
		t.Error("写入失败时不应创建目标文件")
	}
}

// rename 失败时要清掉已写好的 .tmp，不能留残留。
// 让目标路径是个目录 → rename 必然 EISDIR，任何 uid 都能确定性触发。
func TestStoreRemovesTmpWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	target := filepath.Join(dir, "weather.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWeather(&state.Weather{Text: "x"}); err == nil {
		t.Fatal("rename 到目录应失败")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("失败后残留 .tmp 文件: %v", err)
	}
}

// 写入失败时不能破坏已有文件
func TestStoreKeepsOldFileOnWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Log("root 用户不受目录权限限制，本环境无法验证只读目录语义；" +
			"确定性失败路径见 TestStoreCleansTmpOnFailure")
		t.Skip("root 用户不受目录权限限制")
	}
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.SaveToken("good"); err != nil {
		t.Fatal(err)
	}
	// 把目录设为只读，写入应失败
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("无法修改目录权限")
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if err := s.SaveToken("bad"); err == nil {
		t.Error("只读目录下写入应失败")
	}
	tok, err := s.LoadToken()
	if err != nil || tok != "good" {
		t.Errorf("写入失败后旧值应保留，得到 %q / %v", tok, err)
	}
}

func TestStoreCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	s := NewStore(dir)
	if err := s.SaveToken("t"); err != nil {
		t.Fatalf("应自动创建目录: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("目录未创建: %v", err)
	}
}

func TestStoreCorruptFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	os.WriteFile(filepath.Join(dir, "weather.json"), []byte("{not json"), 0o644)
	if _, err := s.LoadWeather(); err == nil {
		t.Error("损坏的 JSON 应报错")
	}
}
