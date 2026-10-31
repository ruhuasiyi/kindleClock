package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/rhsy/kindleclock/src/internal/config"
	"github.com/rhsy/kindleclock/src/internal/display"
	"github.com/rhsy/kindleclock/src/internal/render"
	"github.com/rhsy/kindleclock/src/internal/source"
	"github.com/rhsy/kindleclock/src/internal/state"
	"github.com/rhsy/kindleclock/src/internal/touch"
	"github.com/rhsy/kindleclock/src/internal/ui"
)

// nextMinute 返回「下一个整分」，这是主循环的对齐目标。
func TestNextMinuteAlignsToBoundary(t *testing.T) {
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		{time.Date(2026, 10, 29, 14, 32, 0, 0, time.UTC), time.Date(2026, 10, 29, 14, 33, 0, 0, time.UTC)},
		{time.Date(2026, 10, 29, 14, 32, 30, 0, time.UTC), time.Date(2026, 10, 29, 14, 33, 0, 0, time.UTC)},
		{time.Date(2026, 10, 29, 14, 32, 59, 999, time.UTC), time.Date(2026, 10, 29, 14, 33, 0, 0, time.UTC)},
		{time.Date(2026, 10, 29, 23, 59, 10, 0, time.UTC), time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := nextMinute(c.now); !got.Equal(c.want) {
			t.Errorf("nextMinute(%v) = %v, 期望 %v", c.now, got, c.want)
		}
	}
}

// 主循环不能因为网络错误而退出。
func TestLoopSurvivesFetchErrors(t *testing.T) {
	var logged int
	d := &deps{
		cfg: &config.Config{
			Display: config.DisplayConfig{
				FullRefreshEvery: 0, // 测试里不触发全屏刷新
				Rotate:           "cw",
				WorkDir:          t.TempDir(),
			},
			Time: config.TimeConfig{UTCOffsetMinutes: 480},
		},
		renderer: mustRenderer(t),
		fbink:    nil, // 显示层为 nil：只渲染不落盘，专测「网络失败不影响出图」
		differ:   display.NewDiffer(),
		regions:  mustRegions(t),
		logf:     func(format string, args ...any) { logged++ },
		// 抓取失败由后台循环记录，tickOnce 只用 last-good
		fetchWeather: func(ctx context.Context, now time.Time) (*state.Weather, error) {
			return nil, errAlways
		},
		fetchTimetable: func(ctx context.Context, now time.Time) (*state.Timetable, error) {
			return nil, errAlways
		},
		lastWeather:   nil,
		lastTimetable: &state.Timetable{Available: false, Message: "课表暂不可用"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := tickOnce(ctx, d, time.Now()); err != nil {
		t.Fatalf("网络失败不应让 tick 返回错误: %v", err)
	}
	if d.frameCount != 1 {
		t.Errorf("应完成 1 帧，实际 %d", d.frameCount)
	}
	_ = logged // 本路径下 tickOnce 不写日志；保留计数便于日后扩展
}

var errAlways = &staticErr{"boom"}

type staticErr struct{ s string }

func (e *staticErr) Error() string { return e.s }

func mustRegions(t *testing.T) []render.Region {
	t.Helper()
	rs, err := display.MapRegions(render.Regions(), "cw")
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func mustRenderer(t *testing.T) *render.Renderer {
	t.Helper()
	f, err := render.NewFontsFromDataForTest()
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.NewRenderer(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLoadFixtures(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "snap.json")
	os.WriteFile(p, []byte(`{
	  "now": "2026-10-29T14:32:00+08:00",
	  "weather": {"text":"晴","temp":18,"tempMax":21,"tempMin":9,"fetchedAt":"2026-10-29T14:30:00+08:00"},
	  "weatherFresh": true,
	  "timetable": {"available":true,"week":9,"fetchedAt":"2026-10-29T14:30:00+08:00","courses":[]},
	  "nextCourseIdx": -1
	}`), 0o644)

	snap, err := loadSnapshotFile(p)
	if err != nil {
		t.Fatalf("解析 fixtures: %v", err)
	}
	if snap.Now.Hour() != 14 || snap.Weather == nil || snap.Weather.Text != "晴" {
		t.Errorf("解析结果不对: %+v", snap)
	}
}

func TestWritePreviewPNG(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "preview.png")
	img := image.NewGray(image.Rect(0, 0, render.CanvasW, render.CanvasH))
	if err := writePNG(img, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("预览图应写出: %v", err)
	}
	info, _ := os.Stat(out)
	if info.Size() == 0 {
		t.Error("PNG 不应为空")
	}
}

// TestPreviewOutputSize 确认预览链路产出的是横版 1448×1072、非空 PNG。
func TestPreviewOutputSize(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(snapPath, []byte(`{
	  "now": "2026-10-30T14:32:00+08:00",
	  "weather": {"text":"晴","temp":18,"tempMax":21,"tempMin":9,"fetchedAt":"2026-10-30T14:30:00+08:00"},
	  "weatherFresh": true,
	  "timetable": {"available":true,"week":9,"courses":[]},
	  "nextCourseIdx": -1
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "preview.png")

	// 配置路径刻意指向不存在的文件：预览要能在没有配置的新机器上跑
	// （回退到 gofont，中文会缺字形，但链路必须通）。
	if err := runPreview(&options{
		configPath: filepath.Join(dir, "no-such-config.toml"),
		previewTo:  out,
		fixturesTo: snapPath,
	}); err != nil {
		t.Fatalf("预览失败: %v", err)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != render.CanvasW || cfg.Height != render.CanvasH {
		t.Errorf("预览图尺寸 = %dx%d，期望 %dx%d",
			cfg.Width, cfg.Height, render.CanvasW, render.CanvasH)
	}
}

// 课表抓取持续失败时，界面不能永远停在「课表同步中」——
// spec §8 要求的是「课表暂不可用 + 原因码」。但已有可显示数据时要保留
// last-good（502 不打扰用户），所以只有「当前没有可用课表」才推动界面状态。
func TestMarkTimetableUnavailable(t *testing.T) {
	cause := &staticErr{"请求 dashboard: connection refused"}

	// 冷启动、缓存里没有课表：应把界面从「同步中」推进到「暂不可用」
	d := &deps{lastTimetable: &state.Timetable{Available: false, Message: "课表同步中"}}
	markTimetableUnavailable(d, cause)
	cur := d.getTimetable()
	if cur.Available {
		t.Errorf("界面课表仍为可用: %+v", cur)
	}
	if !strings.HasPrefix(cur.Message, "课表暂不可用") {
		t.Errorf("Message = %q，应以「课表暂不可用」开头", cur.Message)
	}
	// 原因码要带上 —— 否则断网时用户只知道「不可用」，不知道为什么
	if !strings.Contains(cur.Message, "connection refused") {
		t.Errorf("Message = %q，应带原因码", cur.Message)
	}

	// 有 last-good：界面保持原样
	fetched := time.Date(2026, 10, 30, 14, 30, 0, 0, time.UTC)
	prev := &state.Timetable{
		Available: true,
		Courses:   []state.Course{{Name: "大学物理", StartText: "14:15", EndText: "14:55"}},
		FetchedAt: fetched,
	}
	d = &deps{lastTimetable: prev}
	markTimetableUnavailable(d, cause)
	if cur := d.getTimetable(); !cur.Available || len(cur.Courses) != 1 {
		t.Errorf("界面数据被改写了: %+v —— 有 last-good 时不应动它", cur)
	}
}

// 原因码要能塞进课程区一行，超长必须截断。
func TestShortErrTruncates(t *testing.T) {
	long := &staticErr{strings.Repeat("x", 100)}
	got := shortErr(long)
	if len([]rune(got)) > 41 {
		t.Errorf("shortErr 未截断，长度 %d: %q", len([]rune(got)), got)
	}
}

// fakeFbink 生成一个只把参数写进日志的假 fbink，返回调用记录读取函数。
//
// 用脚本而不是 mock：display.Fbink 是具体类型（内部直接 exec），
// 没有接口可替身。假脚本能同时验证参数拼装（-c / file= / x= / -W），
// 比只断言「调了几次」更有判别力。
func fakeFbink(t *testing.T) (*display.Fbink, func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	script := filepath.Join(dir, "fbink")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	read := func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	return display.NewFbink(script), read
}

// newTestDeps 造一份能真正跑到 fbink 的 deps（渲染器用内置 gofont）。
func newTestDeps(t *testing.T, fbink *display.Fbink) *deps {
	t.Helper()
	return &deps{
		cfg: &config.Config{
			Display: config.DisplayConfig{
				FullRefreshEvery: 0,
				Rotate:           "cw",
				WorkDir:          t.TempDir(),
			},
			Time: config.TimeConfig{UTCOffsetMinutes: 480},
		},
		renderer:      mustRenderer(t),
		fbink:         fbink,
		differ:        display.NewDiffer(),
		regions:       mustRegions(t),
		logf:          func(format string, args ...any) {},
		lastTimetable: &state.Timetable{Available: false, Message: "课表暂不可用"},
		// 测试绝不碰真机：背光读写默认走假实现，需要特定值/失败的测试
		// 在自己的 deps 上覆盖这两个字段。
		readIntensity:  func() (int, error) { return 12, nil },
		writeIntensity: func(int) error { return nil },
		// 手动刷新课表的完成通知。默认容量 1；测试若不等消费方，
		// 非阻塞发送也不会把后台 goroutine 卡住。
		refreshDone: make(chan struct{}, 1),
		fetchCalendar: func(context.Context, int) (map[string]state.DayInfo, error) {
			return nil, errors.New("测试不联网")
		},
	}
}

// 启动时必须先全屏刷新一次。
//
// 六个刷新区域只覆盖画布 72.5%：左栏有 4 条空白带（y 0-71 / 470-539 /
// 868-961 / 1016-1071）永远不会被局部刷新碰到。不先全屏清一次，
// 启动前屏幕上的任何残留（上一个程序、上次运行的旧帧、GUI 弹窗）就会永久留着。
func TestStartupDoesFullRefresh(t *testing.T) {
	fbink, calls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)

	if err := startupFullRefresh(context.Background(), d, now); err != nil {
		t.Fatalf("首帧全屏刷新失败: %v", err)
	}

	got := calls()
	if len(got) != 1 {
		t.Fatalf("首帧应恰好调用 fbink 一次，实际 %d 次: %v", len(got), got)
	}
	// DrawFull 的特征是 -c（清屏）与全屏 file=；DrawRegion 一定带 x=,y=
	if !strings.Contains(got[0], " -c ") || !strings.Contains(got[0], "file=") {
		t.Errorf("首帧应是全屏刷新（-c + file=），实际 %q", got[0])
	}
	if strings.Contains(got[0], ",x=") {
		t.Errorf("首帧不该是局部刷新，实际 %q", got[0])
	}

	// 全屏之后同一分钟内的第一轮 tickOnce 不应再刷任何区域：
	// 内容与刚才那帧完全相同，再刷一遍只会白闪六下。
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}
	if got := calls(); len(got) != 1 {
		t.Errorf("首帧全屏后同一分钟内不应再局部刷新，多出 %d 次: %v", len(got)-1, got[1:])
	}
}

// fakeFbinkFailingOnce 生成一个假 fbink：参数里含 marker 的调用第一次返回 1，
// 之后返回 0。用来复现「某次局部刷新失败」。
func fakeFbinkFailingOnce(t *testing.T, marker string) (*display.Fbink, func() []string) {
	t.Helper()
	return fakeFbinkFailingTimes(t, marker, 1)
}

// fakeFbinkFailingTimes 生成一个假 fbink：参数里含 marker 的前 times 次调用
// 返回 1，之后返回 0。
func fakeFbinkFailingTimes(t *testing.T, marker string, times int) (*display.Fbink, func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	countPath := filepath.Join(dir, "count")
	script := filepath.Join(dir, "fbink")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + logPath + "'\n" +
		"case \"$*\" in\n" +
		"  *" + marker + "*)\n" +
		"    n=$(cat '" + countPath + "' 2>/dev/null || echo 0)\n" +
		"    n=$((n+1))\n" +
		"    echo $n > '" + countPath + "'\n" +
		"    if [ $n -le " + strconv.Itoa(times) + " ]; then exit 1; fi\n" +
		"    ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	read := func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	return display.NewFbink(script), read
}

// 局部刷新失败后，该区域必须还会被重试。
//
// differ.Dirty 在比对时就把本帧指纹写了进去，而 DrawRegion 之后才可能失败。
// 不处理的话 differ 认为「这块已经是最新的」，只要内容不变（日历、天气、
// 课程……除时钟外的全部区域）就永远不会重试，屏幕永久停在旧内容。
func TestPartialRefreshFailureIsRetried(t *testing.T) {
	fbink, calls := fakeFbinkFailingOnce(t, "frame-calendar")
	d := newTestDeps(t, fbink)
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)

	calendarCalls := func() int {
		n := 0
		for _, c := range calls() {
			if strings.Contains(c, "frame-calendar") {
				n++
			}
		}
		return n
	}

	for i := 0; i < 3; i++ {
		if err := tickOnce(context.Background(), d, now); err != nil {
			t.Fatalf("第 %d 轮 tickOnce: %v", i+1, err)
		}
		if got := calendarCalls(); got >= 2 {
			break
		}
	}
	if got := calendarCalls(); got < 2 {
		t.Errorf("calendar 区域只被刷新 %d 次，失败后没有重试", got)
	}
}

// fakeFbinkAlwaysFailing 生成一个假 fbink：参数里含 marker 的调用永远返回 1。
func fakeFbinkAlwaysFailing(t *testing.T, marker string) *display.Fbink {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fbink")
	body := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *" + marker + "*) exit 1 ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return display.NewFbink(script)
}

// fakeFbinkToggleable 生成一个假 fbink：flagPath 存在时 calendar 区域失败，
// 不存在时全部成功。测试通过创建/删除该文件切换失败状态。
func fakeFbinkToggleable(t *testing.T) (fbink *display.Fbink, calls func() []string, failOn, heal func()) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	flagPath := filepath.Join(dir, "failing")
	script := filepath.Join(dir, "fbink")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + logPath + "'\n" +
		"if [ -f '" + flagPath + "' ]; then\n" +
		"  case \"$*\" in\n" +
		"    *frame-calendar*) exit 1 ;;\n" +
		"  esac\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	read := func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	failOn = func() {
		if err := os.WriteFile(flagPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	heal = func() {
		if err := os.Remove(flagPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	return display.NewFbink(script), read, failOn, heal
}

func countFull(calls []string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, " -c ") {
			n++
		}
	}
	return n
}

// 整个失败区间只试一次全屏。
//
// 全屏救得回来就自然清零，不需要第二次；救不回来（fbink 整个坏了、
// EPDC 堵死）第 2 次和第 8 次也一样失败，多付的闪烁纯属白给。
func TestFullRefreshOncePerFailureRun(t *testing.T) {
	fbink, read, failOn, _ := fakeFbinkToggleable(t)
	d := newTestDeps(t, fbink)
	failOn()
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		if err := tickOnce(context.Background(), d, now); err != nil {
			t.Fatalf("第 %d 轮: %v", i+1, err)
		}
	}
	if got := countFull(read()); got != 1 {
		t.Errorf("连续失败 5 次，全屏兜底应只发生 1 次，实际 %d 次", got)
	}
	if d.displayFailures != 5 {
		t.Errorf("displayFailures = %d，期望 5", d.displayFailures)
	}
}

// 失败区间结束后重新计数：清零后应能再次触发全屏。
//
// 这条杀掉「fullRefreshTried 永不清零」—— 那种实现下第二次区间
// 不会再兜底，用户只能干等 10 次阈值。
func TestFullRefreshTriedResetsAfterSuccess(t *testing.T) {
	fbink, read, failOn, heal := fakeFbinkToggleable(t)
	d := newTestDeps(t, fbink)
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)

	failOn()
	// 第 1、2 次失败 → 触发一次全屏
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if got := countFull(read()); got != 1 {
		t.Fatalf("连续失败 2 次应全屏 1 次，实际 %d 次", got)
	}

	// 恢复成功一帧 → 计数与一次性标记都清零
	heal()
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if d.displayFailures != 0 {
		t.Fatalf("成功一帧后 displayFailures 应为 0，实际 %d", d.displayFailures)
	}

	// 换一天：日期与月历内容都变，calendar 重新变脏；再失败 2 次应再全屏
	failOn()
	later := now.Add(24 * time.Hour)
	if err := tickOnce(context.Background(), d, later); err != nil {
		t.Fatal(err)
	}
	if err := tickOnce(context.Background(), d, later); err != nil {
		t.Fatal(err)
	}
	if got := countFull(read()); got != 2 {
		t.Errorf("清零后再次失败 2 次应再全屏，累计应为 2，实际 %d 次", got)
	}
}

// GUI 交还后是一次全新的失败区间：全屏兜底应能再试一次。
//
// 这条杀掉「GUI 阈值路径忘了清 fullRefreshTried」—— 那种实现下，
// 交还 GUI 之后的每一次失败区间都不会再有全屏兜底。
func TestFullRefreshTriedResetsAfterGUIRestore(t *testing.T) {
	fbink, read, failOn, _ := fakeFbinkToggleable(t)
	d := newTestDeps(t, fbink)
	failOn()

	restored := 0
	old := restoreGUI
	restoreGUI = func() error { restored++; return nil }
	defer func() { restoreGUI = old }()

	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)

	// 连败 10 次：第 2 次触发一次全屏，第 10 次交还 GUI
	for i := 0; i < guiRestoreThreshold; i++ {
		if err := tickOnce(context.Background(), d, now); err != nil {
			t.Fatalf("第 %d 轮: %v", i+1, err)
		}
	}
	if restored != 1 {
		t.Fatalf("连败 10 次应恢复 GUI 一次，实际 %d 次", restored)
	}
	if got := countFull(read()); got != 1 {
		t.Fatalf("交还前应只全屏 1 次，实际 %d 次", got)
	}

	// 交还后继续失败 2 次：新一轮区间，应再全屏一次
	for i := 0; i < displayRetryFullRefreshAt; i++ {
		if err := tickOnce(context.Background(), d, now); err != nil {
			t.Fatalf("交还后第 %d 轮: %v", i+1, err)
		}
	}
	if got := countFull(read()); got != 2 {
		t.Errorf("GUI 交还后再失败 2 次应再全屏一次，累计应为 2，实际 %d 次", got)
	}
}

// 连续失败到 2 次时要试一次全屏刷新。
//
// 单次瞬时失败（信号打断、EPDC 队列满）下一帧局部重试就能过去，
// 为它付一次 GC16 闪烁不值得；但持续但局部的失败（裁切错、PNG 写坏）
// 若只重试，会静默停在旧内容上直到第 10 次把 GUI 整个还回去 ——
// 中间缺一档「整屏重画试试」。
func TestSecondFailureTriggersFullRefresh(t *testing.T) {
	fbink, calls := fakeFbinkFailingTimes(t, "frame-calendar", 2)
	d := newTestDeps(t, fbink)
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)

	fullCalls := func() int {
		n := 0
		for _, c := range calls() {
			if strings.Contains(c, " -c ") {
				n++
			}
		}
		return n
	}

	// 第 1 次失败：只重置 differ，不该全屏
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if got := fullCalls(); got != 0 {
		t.Fatalf("第 1 次失败就全屏刷新了（%d 次）", got)
	}

	// 第 2 次失败：升级为一次全屏
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if got := fullCalls(); got != 1 {
		t.Errorf("连续失败 2 次应全屏刷新一次，实际 %d 次", got)
	}
}

// fakeFbinkFailingRegionAndFull 生成一个假 fbink：calendar 区域与全屏
// （带 -c 的调用）都失败，其余区域正常。
func fakeFbinkFailingRegionAndFull(t *testing.T) *display.Fbink {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fbink")
	body := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *frame-calendar*) exit 1 ;;\n" +
		"  *\" -c \"*) exit 1 ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return display.NewFbink(script)
}

// 全屏刷新本身失败时不能崩，也不能把它算进 displayFailures ——
// 阈值 10 是按「局部刷新失败」定的；算进去会让一个坏掉的 fbink
// 提前好几帧就把 GUI 还回去。
func TestFullRefreshFailureDoesNotCrashOrCount(t *testing.T) {
	d := newTestDeps(t, fakeFbinkFailingRegionAndFull(t))

	restored := 0
	old := restoreGUI
	restoreGUI = func() error { restored++; return nil }
	defer func() { restoreGUI = old }()

	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := tickOnce(context.Background(), d, now); err != nil {
			t.Fatalf("第 %d 轮不应返回错误: %v", i+1, err)
		}
	}
	// 3 轮各 1 个区域失败 → 3；第 2 轮触发的全屏刷新失败不计入
	if d.displayFailures != 3 {
		t.Errorf("displayFailures = %d，期望 3（全屏失败不计入）", d.displayFailures)
	}
	if restored != 0 {
		t.Errorf("3 次失败不该恢复 GUI")
	}
}

// 显示连续失败到阈值时要把原生界面还回去，避免屏幕永久停在坏帧。
func TestDisplayFailuresRestoreGUI(t *testing.T) {
	d := newTestDeps(t, fakeFbinkAlwaysFailing(t, "frame-calendar"))

	restored := 0
	old := restoreGUI
	restoreGUI = func() error { restored++; return nil }
	defer func() { restoreGUI = old }()

	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)

	// 失败 1 次不该触发
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if d.displayFailures != 1 {
		t.Errorf("displayFailures = %d，期望 1", d.displayFailures)
	}
	if restored != 0 {
		t.Errorf("失败 1 次就恢复 GUI 了")
	}

	// 再失败 9 次
	for i := 0; i < 9; i++ {
		if err := tickOnce(context.Background(), d, now); err != nil {
			t.Fatal(err)
		}
	}
	if restored != 1 {
		t.Errorf("连续失败 10 次应恢复 GUI 一次，实际 %d 次", restored)
	}
	if d.displayFailures != 0 {
		t.Errorf("恢复 GUI 后计数应清零，实际 %d", d.displayFailures)
	}
}

// 单帧渲染错误不能让主循环退出 —— 时钟是这个设备的最低承诺。
func TestRunLoopSurvivesTickErrors(t *testing.T) {
	d := newTestDeps(t, nil)
	// 故意给一个非法方向：每轮 tickOnce 都会返回错误
	d.cfg.Display.Rotate = "sideways"

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, d) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runLoop 应返回 nil，得到 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runLoop 没有在 ctx 取消后退出")
	}
}

// -preview 不带 -fixtures 必须报错（否则会偷偷联网抓取）。
func TestPreviewRequiresFixtures(t *testing.T) {
	if _, err := parseFlags([]string{"-preview", "/tmp/x.png"}); err == nil {
		t.Fatal("缺少 -fixtures 时应报错")
	}
	if _, err := parseFlags([]string{"-preview", "/tmp/x.png", "-fixtures", "/tmp/y.json"}); err != nil {
		t.Errorf("两个都给时应通过，得到 %v", err)
	}
}

// 抓取侧必须用配置时区的 now。
//
// 设备的时区名是 "GMT+8:0100"，不是 IANA 名，Go 解析失败会回落成 UTC。
// 若抓取侧用 time.Now()（系统本地时区）、显示侧用 localZone，
// 课程绝对时间会整体偏 8 小时 —— 而时钟本身仍然正确，最难排查的一类故障。
func TestFetchUsesConfiguredZone(t *testing.T) {
	cfg := &config.Config{
		Time:    config.TimeConfig{UTCOffsetMinutes: 480},
		Weather: config.WeatherConfig{RefreshMinutes: 30},
		Timetable: config.TimetableConfig{
			RefreshMinutes: 10,
		},
		Display: config.DisplayConfig{WorkDir: t.TempDir()},
	}
	zones := make(chan string, 4)
	calCalls := make(chan struct{}, 4)
	d := &deps{
		cfg: cfg,
		fetchTimetable: func(_ context.Context, now time.Time) (*state.Timetable, error) {
			select {
			case zones <- now.Location().String():
			default:
			}
			return &state.Timetable{Available: true}, nil
		},
		fetchWeather: func(context.Context, time.Time) (*state.Weather, error) {
			return nil, nil
		},
		// 日历也必须走注入的替身：fetchLoop 里若直接构造真实 fetcher，
		// 这个单测就会发一次对外请求，断网/CI 上是不稳定源。
		fetchCalendar: func(context.Context, int) (map[string]state.DayInfo, error) {
			select {
			case calCalls <- struct{}{}:
			default:
			}
			return nil, errors.New("测试不联网")
		},
		logf:          func(string, ...any) {},
		lastTimetable: &state.Timetable{Available: false},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go fetchLoop(ctx, d, cfg, source.NewStore(t.TempDir()), log.New(io.Discard, "", 0))

	select {
	case got := <-zones:
		if got != "local" {
			t.Errorf("抓取用的时区 = %q，期望配置的 \"local\"", got)
		}
	case <-time.After(time.Second):
		t.Fatal("fetchLoop 没有调用 fetchTimetable")
	}

	// 日历抓取必须也走注入的替身。若 fetchLoop 里直接 new 一个真实
	// fetcher，这个测试仍然会通过 —— 所以这里显式断言替身被调用过。
	select {
	case <-calCalls:
	case <-time.After(time.Second):
		t.Fatal("fetchLoop 没有调用注入的 fetchCalendar（可能走了真实网络）")
	}
}

// 单实例锁：同一锁文件上的第二个 flock 必须失败。
func TestSingleInstanceLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "kindleclock.lock")

	f1, err := acquireLock(lockPath)
	if err != nil {
		t.Fatalf("第一个实例应拿到锁: %v", err)
	}
	defer f1.Close()

	if _, err := acquireLock(lockPath); err == nil {
		t.Fatal("第二个实例不该拿到锁")
	}
}

// TestVersionFlag 确认版本输出带程序名（printVersion 不 os.Exit，便于捕获）。
func TestVersionFlag(t *testing.T) {
	out := captureOutput(t, func() { printVersion() })
	if !strings.Contains(out, "kindleclock") {
		t.Errorf("版本输出应包含程序名，得到 %q", out)
	}
}

func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var sb strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

// 关掉课表和「还没抓到第一份」是两种不同的状态，界面不该说同一句话。
//
// 真机上发现：config 里 timetable.enabled = false 时，课程区永远显示
// 「课表同步中」—— 它在暗示自己正在同步，实际永远不会。用户会一直等。
func TestInitialTimetableDistinguishesDisabled(t *testing.T) {
	disabled := initialTimetable(&config.Config{
		Timetable: config.TimetableConfig{Enabled: false},
	})
	if disabled.Available {
		t.Fatal("关掉课表时 Available 应为 false")
	}
	if disabled.Message != "课表未启用" {
		t.Fatalf("关掉课表时应显示「课表未启用」，得到 %q", disabled.Message)
	}

	enabled := initialTimetable(&config.Config{
		Timetable: config.TimetableConfig{Enabled: true},
	})
	if enabled.Message != "课表同步中" {
		t.Fatalf("开着但还没抓到数据时应显示「课表同步中」，得到 %q", enabled.Message)
	}
}

// getTimetable 在 lastTimetable 还是 nil 时也要走同一条判断。
func TestGetTimetableFallsBackToInitialState(t *testing.T) {
	d := &deps{cfg: &config.Config{
		Timetable: config.TimetableConfig{Enabled: false},
	}}
	if got := d.getTimetable(); got.Message != "课表未启用" {
		t.Fatalf("期望「课表未启用」，得到 %q", got.Message)
	}
}

// fakeFbinkForToast 造一个只记录参数、永不失败的假 fbink。
func fakeFbinkForToast(t *testing.T) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> '" +
		logPath + "'\nprintf '\\n' >> '" + logPath + "'\nexit 0\n"
	bin = filepath.Join(dir, "fake-fbink")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

// KUAL 的提示文字原来是用 `fbink -m -M` 直接印的，按竖版 framebuffer 坐标画，
// 在横放的设备上整个歪 90 度（真机 status.sh 截图确认）。
// -toast 改成自己把字画成图再旋转，这里验证三件事：
// 真的旋转了、真的居中了、图里真的有墨迹。
func TestToastRotatesAndCenters(t *testing.T) {
	dir := t.TempDir()
	fontPath := filepath.Join(dir, "f.ttf")
	if err := os.WriteFile(fontPath, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}
	fbBin, fbLog := fakeFbinkForToast(t)

	cfg := &config.Config{
		Display: config.DisplayConfig{
			Rotate:      "ccw",
			Fbink:       fbBin,
			FontRegular: fontPath,
			FontBold:    fontPath,
			WorkDir:     dir,
		},
	}

	const text = "KindleClock 已停止"
	if err := renderToast(cfg, text); err != nil {
		t.Fatalf("renderToast: %v", err)
	}

	// 1. 图确实写出来了，而且尺寸是「横版转竖版」后的样子：
	//    横版是 w×h（宽>高），旋转成 ccw 后应该是 h×w（宽<高）。
	f, err := os.Open(filepath.Join(dir, "toast.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() >= b.Dy() {
		t.Fatalf("旋转后应该竖着（宽<高），实际 %dx%d —— 说明没旋转", b.Dx(), b.Dy())
	}

	// 2. 图里必须有墨迹：全白说明字没画上去。
	ink := 0
	gray, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("期望 *image.Gray，得到 %T", img)
	}
	for _, v := range gray.Pix {
		if v < 0x80 {
			ink++
		}
	}
	if ink == 0 {
		t.Fatal("toast.png 全白，文字没画上去")
	}

	// 3. fbink 被调用，且位置是居中的：x = (CanvasH - w)/2
	data, err := os.ReadFile(fbLog)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	wantX := (render.CanvasH - b.Dx()) / 2
	wantY := (render.CanvasW - b.Dy()) / 2
	if wantX < 0 {
		wantX = 0
	}
	if wantY < 0 {
		wantY = 0
	}
	if !strings.Contains(calls, "x="+strconv.Itoa(wantX)) {
		t.Fatalf("fbink 参数里应有 x=%d，实际调用：\n%s", wantX, calls)
	}
	if !strings.Contains(calls, "y="+strconv.Itoa(wantY)) {
		t.Fatalf("fbink 参数里应有 y=%d，实际调用：\n%s", wantY, calls)
	}
}

// 空白提示应该被拒绝，而不是画一张白图和上去。
func TestToastRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	fontPath := filepath.Join(dir, "f.ttf")
	if err := os.WriteFile(fontPath, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}
	fbBin, _ := fakeFbinkForToast(t)
	cfg := &config.Config{
		Display: config.DisplayConfig{
			Rotate:      "ccw",
			Fbink:       fbBin,
			FontRegular: fontPath,
			FontBold:    fontPath,
			WorkDir:     dir,
		},
	}
	if err := renderToast(cfg, "\n"); err == nil {
		t.Fatal("空提示应该报错")
	}
}

// 造一个假的 sysfs + framebuffer，内容由 content 决定。
func fakeFramebuffer(t *testing.T, content func() *image.Gray) (fbPath, sysDir string) {
	t.Helper()
	dir := t.TempDir()
	sysDir = filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, val := range map[string]string{"stride": "1088", "bits_per_pixel": "8"} {
		if err := os.WriteFile(filepath.Join(sysDir, name), []byte(val+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fbPath = filepath.Join(dir, "fb0")

	g := content()
	buf := make([]byte, 1088*1448)
	for y := 0; y < 1448; y++ {
		copy(buf[y*1088:y*1088+1072], g.Pix[y*1072:(y+1)*1072])
	}
	if err := os.WriteFile(fbPath, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return fbPath, sysDir
}

// expectedPortrait 重建 tickOnce 这一帧会生成的竖版画布，
// 用来当「屏幕上应该是这样」的基准。
func expectedPortrait(t *testing.T, d *deps, now time.Time) *image.Gray {
	t.Helper()
	// 必须与 tickOnce / startupFullRefresh 走同一份组装逻辑，
	// 否则「屏幕上应该是这样」的基准会与真实帧不一致。
	snap := d.buildSnapshot(now)
	land := d.renderer.Render(snap, d.uiState)
	p, err := display.Rotate(land, d.cfg.Display.Rotate)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func countFullRefreshes(calls []string) int {
	n := 0
	for _, c := range calls {
		// fullRefresh 用的是 `-q -c -g file=full.png ...`
		if strings.Contains(c, "full.png") {
			n++
		}
	}
	return n
}

// 屏幕被原生界面盖掉之后必须自愈：全屏重绘一次并把脏区重置。
// 真机现象：手指点一下屏幕，主页盖上来，之后只有时钟区还在更新，
// 表盘其它部分永远停在别人的画面上。
func TestScreenClobberedTriggersFullRefresh(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)
	fbink, calls := fakeFbinkFailingOnce(t, "nonexistent-region")
	_ = fbink
	_ = calls

	fbink2, getCalls := fakeFbink(t)
	d := newTestDeps(t, fbink2)
	d.cfg.Display.VerifyScreen = true

	// 先让屏幕内容是「别人画的」：全白
	blank := image.NewGray(image.Rect(0, 0, 1072, 1448))
	for i := range blank.Pix {
		blank.Pix[i] = 0xFF
	}
	d.fbPath, d.fbSysDir = fakeFramebuffer(t, func() *image.Gray { return blank })

	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if got := countFullRefreshes(getCalls()); got != 1 {
		t.Fatalf("屏幕被盖后应全屏重绘 1 次，实际 %d 次。调用：\n%s",
			got, strings.Join(getCalls(), "\n"))
	}
	if d.lastHealFrame != d.frameCount {
		t.Fatalf("lastHealFrame 应记成当前帧 %d，实际 %d", d.frameCount, d.lastHealFrame)
	}
}

// 屏幕内容正确时不该自愈 —— 否则每分钟都会全屏闪一次。
func TestScreenMatchesNoHeal(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)
	fbink, getCalls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	d.cfg.Display.VerifyScreen = true
	d.fbPath, d.fbSysDir = fakeFramebuffer(t, func() *image.Gray {
		return expectedPortrait(t, d, now)
	})

	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if got := countFullRefreshes(getCalls()); got != 0 {
		t.Fatalf("屏幕内容正确时不该全屏重绘，实际 %d 次", got)
	}
}

// 冷却：连续被盖时最多每 healCooldownFrames 帧自愈一次。
// 没有冷却的话，原生界面每分钟抢屏就会让我们每分钟全屏闪一次。
func TestHealCooldown(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)
	fbink, getCalls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	d.cfg.Display.VerifyScreen = true
	blank := image.NewGray(image.Rect(0, 0, 1072, 1448))
	for i := range blank.Pix {
		blank.Pix[i] = 0xFF
	}
	d.fbPath, d.fbSysDir = fakeFramebuffer(t, func() *image.Gray { return blank })

	// 连续 5 帧：第 1 帧自愈一次，第 2..5 帧在冷却期内不再自愈
	for i := 0; i < healCooldownFrames; i++ {
		if err := tickOnce(context.Background(), d, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := countFullRefreshes(getCalls()); got != 1 {
		t.Fatalf("冷却期内应只自愈 1 次，实际 %d 次", got)
	}

	// 再走一帧，冷却期满，重新自愈
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if got := countFullRefreshes(getCalls()); got != 2 {
		t.Fatalf("冷却期满后应再自愈一次，累计 2 次，实际 %d 次", got)
	}
}

// 读不到 framebuffer 时不要自愈 —— 这是检测机制，
// 不能因为检测失败反而把好好的屏幕重画一遍。
func TestHealSkippedWhenFramebufferUnreadable(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)
	fbink, getCalls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	d.cfg.Display.VerifyScreen = true
	d.fbPath = "/nonexistent/fb0"
	d.fbSysDir = "/nonexistent/sys"

	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if got := countFullRefreshes(getCalls()); got != 0 {
		t.Fatalf("读不到 framebuffer 时不该全屏重绘，实际 %d 次", got)
	}
}

// 关掉 verify_screen 时完全不读 framebuffer。
func TestHealDisabled(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)
	fbink, getCalls := fakeFbink(t)
	d := newTestDeps(t, fbink) // VerifyScreen 默认 false
	blank := image.NewGray(image.Rect(0, 0, 1072, 1448))
	for i := range blank.Pix {
		blank.Pix[i] = 0xFF
	}
	d.fbPath, d.fbSysDir = fakeFramebuffer(t, func() *image.Gray { return blank })

	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatal(err)
	}
	if got := countFullRefreshes(getCalls()); got != 0 {
		t.Fatalf("关掉 verify_screen 后不该自愈，实际 %d 次", got)
	}
}

// 独占模式下原生界面被重新拉起来时，我们要让出屏幕而不是和它抢。
//
// 抢的后果很具体：自愈每 5 分钟把表盘盖回用户正在看的书上。
func TestYieldsWhenGUIRestarts(t *testing.T) {
	fbink, _ := fakeFbink(t)
	d := newTestDeps(t, fbink)
	d.cfg.Display.StopGUIOnStart = true
	d.guiRunning = func() bool { return true }

	err := tickOnce(context.Background(), d,
		time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC))
	if !errors.Is(err, errYieldToGUI) {
		t.Fatalf("GUI 在跑时应返回 errYieldToGUI，得到 %v", err)
	}
}

// 非独占模式不看 GUI 状态 —— 那种模式下界面本来就在跑，看它会让时钟永不显示。
func TestDoesNotYieldWithoutExclusiveMode(t *testing.T) {
	fbink, _ := fakeFbink(t)
	d := newTestDeps(t, fbink)
	d.cfg.Display.StopGUIOnStart = false
	d.guiRunning = func() bool { return true }

	if err := tickOnce(context.Background(), d,
		time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)); err != nil {
		t.Fatalf("非独占模式不该让出，得到 %v", err)
	}
}

// 独占模式下 GUI 没跑时正常渲染。
func TestRendersWhenGUIStopped(t *testing.T) {
	fbink, getCalls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	d.cfg.Display.StopGUIOnStart = true
	d.guiRunning = func() bool { return false }

	if err := tickOnce(context.Background(), d,
		time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)); err != nil {
		t.Fatalf("GUI 停着时应正常渲染，得到 %v", err)
	}
	if len(getCalls()) == 0 {
		t.Fatal("应该调用了 fbink，实际一次都没有")
	}
}

// 主循环收到让出信号时，应当当作正常收尾而不是错误。
func TestRunLoopExitsCleanlyOnYield(t *testing.T) {
	fbink, _ := fakeFbink(t)
	d := newTestDeps(t, fbink)
	d.cfg.Display.StopGUIOnStart = true
	d.guiRunning = func() bool { return true }

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := runLoop(ctx, d); err != nil {
		t.Fatalf("让出屏幕应算正常退出，得到 %v", err)
	}
}

// buildSnapshot 是 tickOnce 与 startupFullRefresh 共用的组装入口：
// 两处都必须带上 Day 与 Battery，否则开机首屏会「先空一下再出现」。
func TestBuildSnapshotIncludesDayAndBattery(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "capacity"), []byte("64\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "status"), []byte("Charging\n"), 0o644)

	d := newTestDeps(t, nil)
	d.cfg.Display.ShowBattery = true
	d.batteryDir = dir
	d.mergeCalendar(2026, map[string]state.DayInfo{
		"2026-11-07": {SolarTerm: "立冬", LunarMonthName: "九月", LunarDayName: "廿九"},
	})

	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	snap := d.buildSnapshot(now)
	if snap.Day.SolarTerm != "立冬" {
		t.Errorf("snap.Day.SolarTerm = %q，期望立冬", snap.Day.SolarTerm)
	}
	if !snap.Battery.Known || snap.Battery.Pct != 64 || !snap.Battery.Charging {
		t.Errorf("snap.Battery 不对: %+v", snap.Battery)
	}

	// 关掉显示开关时不读 sysfs（即便目录里有数据）。
	d.cfg.Display.ShowBattery = false
	if got := d.buildSnapshot(now).Battery; got != (state.Battery{}) {
		t.Errorf("ShowBattery=false 时不应读电量，得到 %+v", got)
	}
}

// 这条现在只验证「首帧会整屏刷一次」。真正守「首帧与 tickOnce 共用
// buildSnapshot」的断言要等渲染层开始消费 Day/Battery 之后才有意义 ——
// 在那之前，两处用不同方式组装快照产出的像素完全相同，任何像素比对都是恒真的。
func TestStartupFullRefreshPaintsOnce(t *testing.T) {
	fbink, calls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	d.mergeCalendar(2026, map[string]state.DayInfo{
		"2026-11-07": {SolarTerm: "立冬"},
	})

	if err := startupFullRefresh(context.Background(), d, now); err != nil {
		t.Fatalf("startupFullRefresh: %v", err)
	}
	if len(calls()) != 1 {
		t.Fatalf("首帧应调用 fbink 一次，实际 %d 次", len(calls()))
	}
}

// 首帧必须与后续帧共用 buildSnapshot 的完整组装逻辑。
//
// 这条断言在 Task 3 时被降级成 smoke：当时渲染层还不读 Day，两处用不同
// 方式组装快照产出的像素完全相同，任何比对都是恒真的。现在渲染层已经消费
// Day（drawDayInfo 把农历/节气行画在 y 205..250），组装的差别会真正落到
// 像素上：如果 startupFullRefresh 绕过 buildSnapshot 自己调 state.Build
// （不带 Day），首帧就少了这一行，两张图必然不同。
func TestStartupFullRefreshUsesBuildSnapshot(t *testing.T) {
	fbink, _ := fakeFbink(t)
	d := newTestDeps(t, fbink)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	// Day 非零 —— 否则这条测试又退化成恒真。
	d.mergeCalendar(2026, map[string]state.DayInfo{
		"2026-11-07": {SolarTerm: "立冬", LunarMonthName: "九月", LunarDayName: "廿九"},
	})

	if err := startupFullRefresh(context.Background(), d, now); err != nil {
		t.Fatalf("startupFullRefresh: %v", err)
	}

	// 首帧实际落盘的图。
	f, err := os.Open(filepath.Join(d.cfg.Display.WorkDir, "full.png"))
	if err != nil {
		t.Fatalf("打开首帧 PNG: %v", err)
	}
	defer f.Close()
	decoded, err := png.Decode(f)
	if err != nil {
		t.Fatalf("解码首帧 PNG: %v", err)
	}
	first, ok := decoded.(*image.Gray)
	if !ok {
		t.Fatalf("首帧 PNG 应是灰度图，得到 %T", decoded)
	}

	// buildSnapshot 独立产出的同一帧。
	landscape := d.renderer.Render(d.buildSnapshot(now), d.uiState)
	want, err := display.Rotate(landscape, d.cfg.Display.Rotate)
	if err != nil {
		t.Fatalf("旋转: %v", err)
	}

	if !first.Bounds().Eq(want.Bounds()) {
		t.Fatalf("首帧尺寸 %v，期望 %v", first.Bounds(), want.Bounds())
	}
	for y := first.Bounds().Min.Y; y < first.Bounds().Max.Y; y++ {
		for x := first.Bounds().Min.X; x < first.Bounds().Max.X; x++ {
			if first.GrayAt(x, y) != want.GrayAt(x, y) {
				t.Fatalf("(%d,%d) 首帧 = %v，buildSnapshot 帧 = %v；"+
					"两处必须用同一份组装逻辑（含 Day）",
					x, y, first.GrayAt(x, y), want.GrayAt(x, y))
			}
		}
	}
}

// 电池读不到时 Known 为 false，渲染层据此不画 —— 不能因为读不到就显示 0%。
func TestReadBattery(t *testing.T) {
	dir := t.TempDir()
	if got := readBattery(dir); got.Known {
		t.Fatalf("目录为空时不该认为读到了: %+v", got)
	}

	os.WriteFile(filepath.Join(dir, "capacity"), []byte("87\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "status"), []byte("Discharging\n"), 0o644)
	got := readBattery(dir)
	if !got.Known || got.Pct != 87 || got.Charging {
		t.Fatalf("放电中应为 87%% 且非充电，得到 %+v", got)
	}

	os.WriteFile(filepath.Join(dir, "status"), []byte("Charging\n"), 0o644)
	if got := readBattery(dir); !got.Charging {
		t.Fatalf("充电中应标记 Charging，得到 %+v", got)
	}

	// 内容不是数字时也不能崩，按读不到处理
	os.WriteFile(filepath.Join(dir, "capacity"), []byte("nope"), 0o644)
	if got := readBattery(dir); got.Known {
		t.Fatalf("容量不是数字时应认为读不到，得到 %+v", got)
	}

	// 越界同样按读不到处理：<0 与 >100 都不该当成合法电量画出去。
	for _, v := range []string{"-1", "101"} {
		os.WriteFile(filepath.Join(dir, "capacity"), []byte(v), 0o644)
		if got := readBattery(dir); got.Known {
			t.Fatalf("容量 %s 越界时应认为读不到，得到 %+v", v, got)
		}
	}

	// 调用方在 batteryDir 为空时跳过读取（readBattery 本身不承担这个判断）。
	os.WriteFile(filepath.Join(dir, "capacity"), []byte("87\n"), 0o644)
	d := newTestDeps(t, nil)
	d.cfg.Display.ShowBattery = true
	d.batteryDir = ""
	if got := d.buildSnapshot(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)).Battery; got != (state.Battery{}) {
		t.Fatalf("batteryDir 为空时不应读电量，得到 %+v", got)
	}
}

// 日历缓存按日期查表；没有数据时返回零值 DayInfo（Line() 为空串，界面不画）。
func TestDayInfoLookup(t *testing.T) {
	d := &deps{lastCalendar: map[string]state.DayInfo{
		"2026-11-07": {SolarTerm: "立冬", LunarMonthName: "九月", LunarDayName: "廿九"},
	}}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if got := d.dayInfo(now); got.SolarTerm != "立冬" {
		t.Fatalf("应命中立冬，得到 %+v", got)
	}
	other := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if got := d.dayInfo(other); got != (state.DayInfo{}) {
		t.Fatalf("没有的日期应返回零值，得到 %+v", got)
	}
	empty := &deps{}
	if got := empty.dayInfo(now); got != (state.DayInfo{}) {
		t.Fatalf("lastCalendar 为 nil 时应返回零值，得到 %+v", got)
	}
}

// 12-31 23:00 时若按常规 24 小时睡觉，元旦零点那次抓取会缺席一整天；
// 等待必须缩短到紧贴年边界。
func TestCalendarWaitAlignsToYearBoundary(t *testing.T) {
	const every = 24 * time.Hour

	nyEve := time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)
	if got, want := calendarWait(nyEve, every), time.Hour+5*time.Second; got != want {
		t.Errorf("12-31 23:00 的等待 = %v，期望 %v", got, want)
	}

	midYear := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	if got := calendarWait(midYear, every); got != every {
		t.Errorf("6 月的等待 = %v，期望常规周期 %v", got, every)
	}

	// 元旦当天时下一个边界在一年后，不能算出负数或小于常规周期的值。
	newYear := time.Date(2027, 1, 1, 0, 0, 6, 0, time.UTC)
	if got := calendarWait(newYear, every); got != every {
		t.Errorf("元旦当天的等待 = %v，期望常规周期 %v", got, every)
	}
}

// 抓取可能 12-31 23:59:5x 开始、跨过午夜才结束：抓到的是旧年，
// 若不马上补抓，新年份的数据要再等 24 小时。
func TestNextCalendarWaitRefetchesAfterMidnightCross(t *testing.T) {
	every := 24 * time.Hour

	// 抓的是 2026，但现在已经 2027 —— 说明抓取跨过了午夜，必须马上补抓。
	crossed := time.Date(2027, 1, 1, 0, 0, 3, 0, time.UTC)
	if got := nextCalendarWait(2026, crossed, every); got != 5*time.Second {
		t.Fatalf("跨午夜后应 5 秒补抓，得到 %v", got)
	}

	// 正常情况：抓的年份就是当前年份，按常规周期或年边界走。
	same := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if got := nextCalendarWait(2026, same, every); got != every {
		t.Fatalf("同年应返回常规周期，得到 %v", got)
	}

	// 年末正常对齐年边界。
	yearEnd := time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)
	if got, want := nextCalendarWait(2026, yearEnd, every), time.Hour+5*time.Second; got != want {
		t.Fatalf("年末应等到元旦零点后 5 秒，得到 %v，期望 %v", got, want)
	}
}

// 跨年时表必须按年合并。整表替换会让 2026-12-31 的数据在 2027-01-01
// 消失 —— 这是常驻设备每年元旦必现的农历/节假日缺口。
func TestMergeCalendarKeepsPreviousYear(t *testing.T) {
	d := &deps{}
	d.mergeCalendar(2026, map[string]state.DayInfo{
		"2026-12-31": {LunarMonthName: "冬月", LunarDayName: "十二"},
	})
	d.mergeCalendar(2027, map[string]state.DayInfo{
		"2027-01-01": {HolidayName: "元旦"},
	})

	if !d.hasCalendarYear(2026) || !d.hasCalendarYear(2027) {
		t.Fatalf("2026 与 2027 都应记为已加载")
	}
	if got := d.dayInfo(time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC)); got.LunarFull() != "冬月十二" {
		t.Errorf("合并 2027 后 2026-12-31 应仍可查，得到 %+v", got)
	}
	if got := d.dayInfo(time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)); got.HolidayName != "元旦" {
		t.Errorf("合并后 2027-01-01 应可查，得到 %+v", got)
	}
}

// 查不到就是零值、界面不画，不是崩溃 —— 记录当前行为。
func TestDayInfoMissingYearReturnsZero(t *testing.T) {
	d := &deps{}
	d.mergeCalendar(2026, map[string]state.DayInfo{
		"2026-12-31": {LunarMonthName: "冬月", LunarDayName: "十二"},
	})

	if d.hasCalendarYear(2027) {
		t.Fatal("2027 不应被标记为已加载")
	}
	if got := d.dayInfo(time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)); got != (state.DayInfo{}) {
		t.Fatalf("未加载的年份应返回零值，得到 %+v", got)
	}
}

// 开机时 DNS 还没就绪，第一次抓取必然失败。按正常周期等 10/30 分钟，
// 等于开机后半小时才有数据 —— 真机每次重启的日志里都能看到这两条失败。
func TestFetchWaitRetriesQuicklyAfterEarlyFailure(t *testing.T) {
	cases := []struct {
		every    time.Duration
		failures int
		want     time.Duration
	}{
		{30 * time.Minute, 0, 30 * time.Minute}, // 正常：原周期
		{30 * time.Minute, 1, 30 * time.Second}, // 刚失败：快速重试
		{30 * time.Minute, 2, 30 * time.Second},
		{30 * time.Minute, 3, 5 * time.Minute}, // 连续失败：退避
		{30 * time.Minute, 9, 5 * time.Minute},
		{10 * time.Minute, 1, 30 * time.Second}, // 课表同理
		{10 * time.Minute, 5, 5 * time.Minute},
	}
	for _, c := range cases {
		if got := fetchWait(c.every, c.failures); got != c.want {
			t.Errorf("fetchWait(%v, %d) = %v，期望 %v",
				c.every, c.failures, got, c.want)
		}
	}
}

// ---- Task 8: 主循环接入触摸、课表与设置页 ----

// waitFor 轮询等待条件成立；超时直接失败。用于观察后台协程/主循环的副作用。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// 每类触摸命中都要有正确的状态迁移。
func TestApplyTouchStateTransitions(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	nov2 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		hit  render.Hit
		open bool // 设置页初始状态
		want func(ui.State) bool
	}{
		{"点下个月箭头", render.Hit{Kind: render.HitCalendarNextMonth}, false,
			func(s ui.State) bool { return s.MonthOffset == 1 }},
		{"点上个月箭头", render.Hit{Kind: render.HitCalendarPrevMonth}, false,
			func(s ui.State) bool { return s.MonthOffset == -1 }},
		{"点某一天", render.Hit{Kind: render.HitCalendarDay, Date: nov2}, false,
			func(s ui.State) bool { return !s.FollowToday() && s.MonthOffset == 1 }},
		{"点齿轮", render.Hit{Kind: render.HitSettingsGear}, false,
			func(s ui.State) bool { return s.SettingsOpen }},
		{"点关闭", render.Hit{Kind: render.HitSettingsRow, Row: render.SettingsRowClose}, true,
			func(s ui.State) bool { return !s.SettingsOpen }},
		{"点设备信息", render.Hit{Kind: render.HitSettingsRow, Row: render.SettingsRowInfo}, true,
			func(s ui.State) bool { return s.SettingsOpen }}, // 纯展示，不改状态
		{"点空白", render.Hit{Kind: render.HitNone}, false,
			func(s ui.State) bool { return s.MonthOffset == 0 && !s.SettingsOpen }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDeps(t, nil)
			d.cfg.Periods = testPeriods()
			d.uiState.SettingsOpen = c.open
			d.applyTouch(c.hit, touch.Event{Kind: touch.KindTap}, today)
			if !c.want(d.uiState) {
				t.Fatalf("状态不对: %+v", d.uiState)
			}
		})
	}
}

// 每类触摸都应当把 LastInput 记上，否则 60 秒回位永远不会触发。
func TestApplyTouchRecordsLastInput(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	hits := []render.Hit{
		{Kind: render.HitNone},
		{Kind: render.HitCalendarPrevMonth},
		{Kind: render.HitCalendarNextMonth},
		{Kind: render.HitCalendarDay, Date: today.AddDate(0, 0, 1)},
		{Kind: render.HitCourseArea},
		{Kind: render.HitSettingsGear},
		{Kind: render.HitSettingsRow, Row: render.SettingsRowInfo},
	}
	for _, h := range hits {
		d := newTestDeps(t, nil)
		d.uiState.SettingsOpen = true // 让设置行可点
		d.applyTouch(h, touch.Event{}, today)
		if !d.uiState.LastInput.Equal(today) {
			t.Fatalf("%v: LastInput 应记为 %v，得到 %v", h.Kind, today, d.uiState.LastInput)
		}
	}
}

// 课程滚动要夹紧在合法范围内（不能为负，不能超过页数-1）。
func TestApplyTouchClampsCourseScroll(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	hit := render.Hit{Kind: render.HitCourseArea}

	t.Run("两页", func(t *testing.T) {
		d := newTestDeps(t, nil)
		d.cfg.Periods = testPeriods()
		d.lastTimetable = &state.Timetable{Available: true, Courses: futureCourses(today, 8)}

		d.applyTouch(hit, touch.Event{Kind: touch.KindSwipeDown}, today)
		if d.uiState.CourseScroll != 0 {
			t.Fatalf("第一页再下滑应停在 0，得到 %d", d.uiState.CourseScroll)
		}
		d.applyTouch(hit, touch.Event{Kind: touch.KindSwipeUp}, today)
		if d.uiState.CourseScroll != 1 {
			t.Fatalf("上滑应翻到第 2 页，得到 %d", d.uiState.CourseScroll)
		}
		d.applyTouch(hit, touch.Event{Kind: touch.KindSwipeUp}, today)
		if d.uiState.CourseScroll != 1 {
			t.Fatalf("只有两页，再上滑应停在 1，得到 %d", d.uiState.CourseScroll)
		}
		d.applyTouch(hit, touch.Event{Kind: touch.KindSwipeDown}, today)
		if d.uiState.CourseScroll != 0 {
			t.Fatalf("下滑应回到第 1 页，得到 %d", d.uiState.CourseScroll)
		}
		// 点击在末页绕回首页，保证只靠点按也能翻页。
		d.applyTouch(hit, touch.Event{Kind: touch.KindTap}, today)
		if d.uiState.CourseScroll != 1 {
			t.Fatalf("点击应翻页，得到 %d", d.uiState.CourseScroll)
		}
		d.applyTouch(hit, touch.Event{Kind: touch.KindTap}, today)
		if d.uiState.CourseScroll != 0 {
			t.Fatalf("末页点击应绕回首页，得到 %d", d.uiState.CourseScroll)
		}
	})

	t.Run("已结束的课不计入页数", func(t *testing.T) {
		d := newTestDeps(t, nil)
		d.cfg.Periods = testPeriods()
		courses := futureCourses(today, 7)
		courses[0].End = today // 这一门已结束，剩 6 门 = 1 页
		d.lastTimetable = &state.Timetable{Available: true, Courses: courses}

		d.applyTouch(hit, touch.Event{Kind: touch.KindSwipeUp}, today)
		if d.uiState.CourseScroll != 0 {
			t.Fatalf("剩余课程只有 1 页，上滑不该翻页，得到 %d", d.uiState.CourseScroll)
		}
	})

	t.Run("选中日期的课", func(t *testing.T) {
		d := newTestDeps(t, nil)
		d.cfg.Periods = testPeriods()
		selected := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) // 周一
		d.uiState.SelectDate(selected, today)
		specs := make([]state.CourseSpec, 8)
		for i := range specs {
			specs[i] = state.CourseSpec{
				Name: "周一课", DayOfWeek: 1, StartSection: 1, EndSection: 1,
				Weeks: []int{state.WeekOf(&state.Schedule{Week: 9, FetchedAt: today}, selected)},
			}
		}
		d.setSchedule(&state.Schedule{Week: 9, MaxWeek: 20, FetchedAt: today, Specs: specs})

		d.applyTouch(hit, touch.Event{Kind: touch.KindSwipeUp}, today)
		if d.uiState.CourseScroll != 1 {
			t.Fatalf("选中日期有 8 门课（2 页），上滑应到第 2 页，得到 %d", d.uiState.CourseScroll)
		}
	})
}

// 手势按**落手点**做命中，不是抬指点。
//
// 反例：从课程区第一行向上滑，抬手会落进日历网格 ——
// 按抬指点判定就会误选一个日期，而用户只是想翻页。
func TestGestureHitsStartPointNotEndPoint(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	monthStart := ui.MonthStart(today, 0)

	ev := touch.Event{
		Kind: touch.KindSwipeUp,
		// 落手在课程区第一行，向上滑后抬手落进日历网格。
		StartX: 900, StartY: 700,
		X: 900, Y: 400,
	}
	// 先钉死测试前提：抬指点确实会被判成日历格，否则这条测试没有判别力。
	if h := render.HitTest(ev.X, ev.Y, monthStart, false); h.Kind != render.HitCalendarDay {
		t.Fatalf("测试前提不成立：抬指点应命中日历格，实际 %v", h.Kind)
	}

	d := newTestDeps(t, nil)
	d.cfg.Periods = testPeriods()
	d.lastTimetable = &state.Timetable{Available: true, Courses: futureCourses(today, 8)}

	hit := gestureHit(ev, monthStart, false)
	if hit.Kind != render.HitCourseArea {
		t.Fatalf("落手点应命中课程区，实际 %v", hit.Kind)
	}
	d.applyTouch(hit, ev, today)

	if d.uiState.CourseScroll != 1 {
		t.Fatalf("上滑应翻到第 2 页，得到 CourseScroll=%d", d.uiState.CourseScroll)
	}
	if !d.uiState.FollowToday() || !d.uiState.SelectedDate.IsZero() {
		t.Fatalf("翻页不应选中日期，得到 %+v", d.uiState)
	}
}

// 非课程区的目标只接受点击，滑动不得触发动作。
//
// 反例：设置页 Row 0 里一次竖直拖动会执行「返回系统」，时钟直接退出；
// 滑过日历格子会误选日期。
func TestSwipeDoesNotTriggerNonCourseActions(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	swipe := touch.Event{Kind: touch.KindSwipeUp, X: 700, Y: 700, StartX: 700, StartY: 780}

	old := startKindleGUI
	called := 0
	startKindleGUI = func() error { called++; return nil }
	defer func() { startKindleGUI = old }()

	t.Run("设置页 Row 0", func(t *testing.T) {
		d := newTestDeps(t, nil)
		d.uiState.SettingsOpen = true
		d.applyTouch(render.Hit{Kind: render.HitSettingsRow, Row: 0}, swipe, today)
		if called != 0 {
			t.Fatalf("滑动不该启动原生界面，实际调用 %d 次", called)
		}
		if !d.uiState.SettingsOpen {
			t.Fatal("滑动不该关闭设置页")
		}
		if !d.uiState.LastInput.Equal(today) {
			t.Fatal("被忽略的滑动仍应记录 LastInput")
		}
	})

	t.Run("日历格", func(t *testing.T) {
		d := newTestDeps(t, nil)
		day := today.AddDate(0, 0, 1)
		d.applyTouch(render.Hit{Kind: render.HitCalendarDay, Date: day}, swipe, today)
		if !d.uiState.FollowToday() {
			t.Fatalf("滑动不该选中日期，得到 %+v", d.uiState)
		}
	})

	t.Run("齿轮", func(t *testing.T) {
		d := newTestDeps(t, nil)
		d.applyTouch(render.Hit{Kind: render.HitSettingsGear}, swipe, today)
		if d.uiState.SettingsOpen {
			t.Fatal("滑动不该打开设置页")
		}
	})
}

func TestApplySettingsRowsFollowOrder(t *testing.T) {
	old := startKindleGUI
	called := 0
	startKindleGUI = func() error { called++; return nil }
	defer func() { startKindleGUI = old }()

	d := newTestDeps(t, nil)
	d.uiState.SettingsOpen = true
	var setTo []int
	d.readIntensity = func() (int, error) { return 12, nil }
	d.writeIntensity = func(v int) error { setTo = append(setTo, v); return nil }
	var schedCalls, ttCalls atomic.Int32
	d.fetchSchedule = func(context.Context, time.Time) (*state.Schedule, error) {
		schedCalls.Add(1)
		return &state.Schedule{Week: 9, FetchedAt: time.Date(2026, 10, 30, 19, 20, 0, 0, time.UTC)}, nil
	}
	d.fetchTimetable = func(context.Context, time.Time) (*state.Timetable, error) {
		ttCalls.Add(1)
		return &state.Timetable{Available: true, FetchedAt: time.Date(2026, 10, 30, 19, 20, 0, 0, time.UTC)}, nil
	}

	// Row 2（背光亮度）：不是「设备信息」—— 点轨道右端必须写 lipc 并强制刷新。
	d.applySettingsRow(render.SettingsRowBrightness, render.SliderTrackX+render.SliderTrackW)
	if len(setTo) != 1 || setTo[0] != brightnessMax {
		t.Fatalf("Row 2 应是背光亮度（点右端 → %d），实际写入 %v", brightnessMax, setTo)
	}
	if !d.forceFullRefresh {
		t.Fatal("调完亮度应强制整屏刷新")
	}
	d.forceFullRefresh = false

	// Row 3（刷新课表）：两条线各触发一次异步抓取，仍停在设置页。
	d.applySettingsRow(render.SettingsRowRefreshSchedule, 0)
	waitFor(t, 2*time.Second, func() bool {
		return schedCalls.Load() == 1 && ttCalls.Load() == 1
	}, "Row 3 应各触发一次整学期/今日课表抓取")
	select {
	case <-d.refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("课表刷新完成后应通知主循环")
	}
	d.forceFullRefresh = false // 收掉 Row 3 启动时的刷新标志，免得干扰下面的断言

	// Row 4（设备信息）：纯展示，既不改状态也不写背光/抓取。
	d.applySettingsRow(render.SettingsRowInfo, 0)
	if !d.uiState.SettingsOpen || d.forceFullRefresh || len(setTo) != 1 ||
		schedCalls.Load() != 1 || ttCalls.Load() != 1 || called != 0 {
		t.Fatalf("Row 4 应无动作，得到 %+v force=%v set=%v sched=%d tt=%d called=%d",
			d.uiState, d.forceFullRefresh, setTo, schedCalls.Load(), ttCalls.Load(), called)
	}
	// Row 1（立即全屏刷新）：置全屏刷新与闪屏标志，仍停在设置页。
	d.applySettingsRow(render.SettingsRowRefresh, 0)
	if !d.uiState.SettingsOpen || !d.forceFullRefresh || !d.flashFullRefresh || called != 0 {
		t.Fatalf("Row 1 应触发带闪屏的全屏刷新，得到 %+v force=%v flash=%v called=%d",
			d.uiState, d.forceFullRefresh, d.flashFullRefresh, called)
	}
	// Row 0（返回系统）：启动原生界面、立刻标记退出、并写下让出标记。
	d.applySettingsRow(render.SettingsRowBack, 0)
	if called != 1 {
		t.Fatalf("Row 0 应启动原生界面，实际 %d 次", called)
	}
	if !d.yieldToGUI {
		t.Fatal("Row 0 应立刻置 yieldToGUI —— 等下一轮 tick 会把表盘画回屏幕")
	}
	if _, err := os.Stat(yieldMarkerPath(d.cfg.Display.WorkDir)); err != nil {
		t.Fatalf("Row 0 应写下让出标记: %v", err)
	}
	// Row 5（关闭）：只关设置页，绝不能顺手调亮度或触发抓取。
	d.applySettingsRow(render.SettingsRowClose, 0)
	if d.uiState.SettingsOpen {
		t.Fatalf("Row 5 应关闭设置页，得到 %+v", d.uiState)
	}
	if len(setTo) != 1 {
		t.Fatalf("点「关闭」不该写背光，实际写入 %v —— Row 索引错位了", setTo)
	}
	if schedCalls.Load() != 1 || ttCalls.Load() != 1 {
		t.Fatalf("点「关闭」不该触发课表抓取，实际整学期 %d / 今日 %d —— Row 索引错位了",
			schedCalls.Load(), ttCalls.Load())
	}
}

// 点「刷新课表」立刻返回，抓取在后台完成；成功后更新 lastSchedule、
// 清掉刷新中、并通知主循环重画。
//
// 变异：去掉 goroutine 改成同步抓取 → 本测试前面的「立刻返回」断言会
// 在阻塞型 fetcher 下超时（见下面的 IgnoresWhileRunning）；去掉 setSchedule →
// lastSchedule 仍指向旧值。
func TestStartScheduleRefreshFetchesOnce(t *testing.T) {
	d := newTestDeps(t, nil)
	var schedCalls, ttCalls atomic.Int32
	wantSched := &state.Schedule{Week: 9, FetchedAt: time.Date(2026, 10, 30, 19, 20, 0, 0, time.UTC)}
	wantTT := &state.Timetable{Available: true, FetchedAt: time.Date(2026, 10, 30, 19, 20, 0, 0, time.UTC)}
	d.fetchSchedule = func(context.Context, time.Time) (*state.Schedule, error) {
		schedCalls.Add(1)
		return wantSched, nil
	}
	d.fetchTimetable = func(context.Context, time.Time) (*state.Timetable, error) {
		ttCalls.Add(1)
		return wantTT, nil
	}

	d.startScheduleRefresh()

	// 启动必须立即返回并进入「刷新中」，否则主循环会卡在抓取上。
	if !d.forceFullRefresh {
		t.Fatal("启动刷新应置 forceFullRefresh，让「刷新中…」立刻出现")
	}
	if !d.scheduleRefreshing() {
		t.Fatal("启动后应处于刷新中")
	}

	select {
	case <-d.refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("刷新完成后应通知主循环")
	}

	// 两条线都刷：只刷整学期的话，用户正在看的「今日课程」不会变。
	if schedCalls.Load() != 1 || ttCalls.Load() != 1 {
		t.Fatalf("整学期/今日课表应各抓 1 次，实际 %d / %d",
			schedCalls.Load(), ttCalls.Load())
	}
	if d.getSchedule() != wantSched {
		t.Fatal("成功后 lastSchedule 应指向新抓取的课表")
	}
	if d.getTimetable() != wantTT {
		t.Fatal("成功后 lastTimetable 应指向新抓取的今日课表")
	}
	if d.scheduleRefreshing() {
		t.Fatal("完成后不应还处于刷新中")
	}
	if d.didScheduleRefreshFail() {
		t.Fatal("成功不应标记失败")
	}
}

// 刷新进行中再点必须忽略：否则连点五下会起五个 goroutine 抢着写缓存。
//
// 变异：删掉 `if d.refreshing { return }` → 阻塞型 fetcher 下 calls 变成 3。
func TestStartScheduleRefreshIgnoresWhileRunning(t *testing.T) {
	d := newTestDeps(t, nil)
	var schedCalls, ttCalls atomic.Int32
	started := make(chan struct{}, 8) // 容量大于正常情况，突变时不至于把 goroutine 永久卡住
	release := make(chan struct{})
	d.fetchSchedule = func(context.Context, time.Time) (*state.Schedule, error) {
		schedCalls.Add(1)
		started <- struct{}{}
		<-release
		return &state.Schedule{FetchedAt: time.Date(2026, 10, 30, 19, 20, 0, 0, time.UTC)}, nil
	}
	d.fetchTimetable = func(context.Context, time.Time) (*state.Timetable, error) {
		ttCalls.Add(1)
		<-release
		return &state.Timetable{Available: true}, nil
	}

	d.startScheduleRefresh() // 这是「点第一下」
	<-started                // 确保后台已经进入第一个 fetcher

	// 抓取被 release 卡住；这两次调用必须立刻返回且不新增 goroutine。
	// 如果 startScheduleRefresh 是同步的，这里会死等 —— 也验证了异步。
	d.startScheduleRefresh()
	d.startScheduleRefresh()
	// 给「错误实现」的 goroutine 一点时间真的进入 fetcher，再断言没叠加。
	time.Sleep(50 * time.Millisecond)
	if got := schedCalls.Load(); got != 1 {
		t.Fatalf("刷新中重复点击应被忽略，实际抓整整学期课表 %d 次", got)
	}

	close(release)
	select {
	case <-d.refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("刷新完成后应通知主循环")
	}
	if schedCalls.Load() != 1 || ttCalls.Load() != 1 {
		t.Fatalf("整轮应各抓 1 次，实际整学期 %d / 今日 %d",
			schedCalls.Load(), ttCalls.Load())
	}
}

// 任一请求失败都算整体失败；失败那条线保留旧数据，绝不清空。
func TestScheduleRefreshAnyFailureMarksFailed(t *testing.T) {
	oldSched := &state.Schedule{Week: 9, Semester: "旧整学期", FetchedAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)}
	oldTT := &state.Timetable{Available: true, Message: "旧今日", FetchedAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)}
	newSched := &state.Schedule{Week: 9, Semester: "新整学期", FetchedAt: time.Date(2026, 10, 30, 19, 20, 0, 0, time.UTC)}
	newTT := &state.Timetable{Available: true, Message: "新今日", FetchedAt: time.Date(2026, 10, 30, 19, 20, 0, 0, time.UTC)}
	errBoom := errors.New("network down")

	cases := []struct {
		name          string
		schedErr      bool
		ttErr         bool
		wantSchedPtr  *state.Schedule
		wantTimetable *state.Timetable
	}{
		{"整学期失败", true, false, oldSched, newTT},
		{"今日失败", false, true, newSched, oldTT},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDeps(t, nil)
			d.setSchedule(oldSched)
			d.setTimetable(oldTT)
			d.fetchSchedule = func(context.Context, time.Time) (*state.Schedule, error) {
				if c.schedErr {
					return nil, errBoom
				}
				return newSched, nil
			}
			d.fetchTimetable = func(context.Context, time.Time) (*state.Timetable, error) {
				if c.ttErr {
					return nil, errBoom
				}
				return newTT, nil
			}

			d.startScheduleRefresh()
			select {
			case <-d.refreshDone:
			case <-time.After(2 * time.Second):
				t.Fatal("刷新结束后应通知主循环")
			}

			if !d.didScheduleRefreshFail() {
				t.Fatal("任一请求失败都应标记整体失败")
			}
			if d.scheduleRefreshing() {
				t.Fatal("失败后不应还处于刷新中")
			}
			if d.getSchedule() != c.wantSchedPtr {
				t.Fatal("整学期课表的数据来源不对：失败应保留旧值，成功应更新")
			}
			if d.getTimetable() != c.wantTimetable {
				t.Fatal("今日课表的数据来源不对：失败应保留旧值，成功应更新")
			}
		})
	}
}

// 刷新失败：保留旧数据、标记失败、记日志，绝不把 lastSchedule 清空。
//
// 变异：失败分支也把两条线置 nil → 指针比较失败。
func TestScheduleRefreshFailureKeepsLastSchedule(t *testing.T) {
	d := newTestDeps(t, nil)
	old := &state.Schedule{
		Week: 9, Semester: "旧课表",
		FetchedAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC),
	}
	oldTT := &state.Timetable{Available: true, Message: "旧今日"}
	d.setSchedule(old)
	d.setTimetable(oldTT)
	var logs []string
	d.logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	d.fetchSchedule = func(context.Context, time.Time) (*state.Schedule, error) {
		return nil, errors.New("network down")
	}
	d.fetchTimetable = func(context.Context, time.Time) (*state.Timetable, error) {
		return nil, errors.New("network down")
	}

	d.startScheduleRefresh()
	select {
	case <-d.refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("失败后也应通知主循环重画")
	}

	if d.getSchedule() != old {
		t.Fatal("刷新失败绝不能清空/替换原有课表")
	}
	if d.getTimetable() != oldTT {
		t.Fatal("刷新失败绝不能清空/替换原有今日课表")
	}
	if !d.didScheduleRefreshFail() {
		t.Fatal("失败后应标记 ScheduleRefreshFailed")
	}
	if d.scheduleRefreshing() {
		t.Fatal("失败后不应还处于刷新中")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "手动刷新课表失败") {
		t.Fatalf("失败应记日志，实际 %v", logs)
	}
}

// 抓取卡住时 20 秒超时（测试里缩短）必须结束刷新并标记失败。
func TestScheduleRefreshTimesOut(t *testing.T) {
	oldTimeout := scheduleRefreshTimeout
	scheduleRefreshTimeout = 50 * time.Millisecond
	defer func() { scheduleRefreshTimeout = oldTimeout }()

	d := newTestDeps(t, nil)
	d.fetchSchedule = func(ctx context.Context, _ time.Time) (*state.Schedule, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d.fetchTimetable = func(ctx context.Context, _ time.Time) (*state.Timetable, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	d.startScheduleRefresh()
	select {
	case <-d.refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("超时后刷新应结束")
	}
	if !d.didScheduleRefreshFail() {
		t.Fatal("超时应标记失败")
	}
	if d.scheduleRefreshing() {
		t.Fatal("超时后不应还处于刷新中")
	}
}

// 自动抓取成功也要清掉「失败」：后台已经恢复，屏幕上不能一直挂着失败。
func TestAutomaticFetchSuccessClearsRefreshFailure(t *testing.T) {
	d := newTestDeps(t, nil)

	d.mu.Lock()
	d.scheduleRefreshFailed = true
	d.mu.Unlock()
	d.setTimetable(&state.Timetable{Available: true})
	if d.didScheduleRefreshFail() {
		t.Fatal("自动抓取今日课表成功后应清掉失败标记")
	}

	d.mu.Lock()
	d.scheduleRefreshFailed = true
	d.mu.Unlock()
	d.setSchedule(&state.Schedule{Week: 9})
	if d.didScheduleRefreshFail() {
		t.Fatal("自动抓取整学期课表成功后应清掉失败标记")
	}
}

// buildSnapshot 必须把 deps 里的刷新状态带进快照：渲染层只认快照。
func TestBuildSnapshotCarriesScheduleRefreshState(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	d := newTestDeps(t, nil)
	fetchedAt := time.Date(2026, 10, 30, 8, 30, 0, 0, time.UTC)
	d.setSchedule(&state.Schedule{FetchedAt: fetchedAt})

	snap := d.buildSnapshot(today)
	if !snap.Info.ScheduleAt.Equal(fetchedAt) {
		t.Fatalf("Info.ScheduleAt = %v，期望 %v", snap.Info.ScheduleAt, fetchedAt)
	}
	if snap.Info.Refreshing || snap.Info.ScheduleRefreshFailed {
		t.Fatalf("空闲时不应标记刷新中/失败，得到 %+v", snap.Info)
	}

	d.mu.Lock()
	d.refreshing = true
	d.mu.Unlock()
	if snap := d.buildSnapshot(today); !snap.Info.Refreshing {
		t.Fatal("deps.refreshing 应带进 Info.Refreshing")
	}

	d.mu.Lock()
	d.refreshing = false
	d.scheduleRefreshFailed = true
	d.mu.Unlock()
	if snap := d.buildSnapshot(today); !snap.Info.ScheduleRefreshFailed {
		t.Fatal("deps.scheduleRefreshFailed 应带进 Info.ScheduleRefreshFailed")
	}
}

// 变异：去掉向左/向右的夹紧 → 两个越界用例分别得到负数 / 超过 24；
// 把四舍五入改成截断 → 中点用例仍过，所以另用 x 恰好越过半档的位置兜底。
func TestBrightnessLevelAt(t *testing.T) {
	cases := []struct {
		name string
		x    int
		want int
	}{
		{"轨道左端", render.SliderTrackX, 0},
		{"轨道右端", render.SliderTrackX + render.SliderTrackW, brightnessMax},
		{"轨道中点", render.SliderTrackX + render.SliderTrackW/2, brightnessMax / 2},
		{"左端外", render.SliderTrackX - 300, 0},
		{"右端外", render.SliderTrackX + render.SliderTrackW + 200, brightnessMax},
		// 20px ≈ 0.73 档：四舍五入应得 1，截断会得 0。
		{"不足一档向上取整", render.SliderTrackX + 20, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := brightnessLevelAt(c.x); got != c.want {
				t.Fatalf("brightnessLevelAt(%d) = %d，期望 %d", c.x, got, c.want)
			}
		})
	}
}

// 点击/拖动到某个 x 后写入对应档位；点在当前档位上不写、不刷新。
func TestAdjustBrightnessFromX(t *testing.T) {
	cases := []struct {
		name    string
		start   int
		x       int
		wantSet []int
	}{
		{"点到左端", 12, render.SliderTrackX, []int{0}},
		{"点到中点", 20, render.SliderTrackX + render.SliderTrackW/2, []int{12}},
		{"点到右端", 12, render.SliderTrackX + render.SliderTrackW, []int{brightnessMax}},
		{"点到当前档", 12, render.SliderTrackX + render.SliderTrackW/2, nil},
		{"越界左夹到 0", 0, render.SliderTrackX - 300, nil},
		{"越界右夹到上限", brightnessMax, render.SliderTrackX + render.SliderTrackW + 300, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDeps(t, nil)
			d.uiState.SettingsOpen = true
			d.readIntensity = func() (int, error) { return c.start, nil }
			var got []int
			d.writeIntensity = func(v int) error {
				got = append(got, v)
				return nil
			}

			d.applySettingsRow(render.SettingsRowBrightness, c.x)

			if len(got) != len(c.wantSet) {
				t.Fatalf("写入档位 = %v，期望 %v", got, c.wantSet)
			}
			for i := range c.wantSet {
				if got[i] != c.wantSet[i] {
					t.Fatalf("写入档位 = %v，期望 %v", got, c.wantSet)
				}
			}
			if len(c.wantSet) > 0 {
				if !d.forceFullRefresh {
					t.Fatal("档位变化后应设 forceFullRefresh，让滑块立刻画到新位置")
				}
			} else if d.forceFullRefresh {
				t.Fatal("档位没变时不该强制刷新")
			}
		})
	}
}

// 拖动按抬手点算，不是落手点：从轨道左端拖到右端，最终应是 24。
// 中途不产生中间帧 —— touch 包每次手势只在抬手时发一个事件，
// 主循环拿到它才重绘一次。
//
// 变异：把拖动分支的 ev.X 改回 ev.StartX → 本用例得到 0。
func TestApplyTouchBrightnessDragUsesEndX(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	monthStart := ui.MonthStart(today, 0)
	y := render.SliderTrackY

	d := newTestDeps(t, nil)
	d.uiState.SettingsOpen = true
	d.readIntensity = func() (int, error) { return 0, nil }
	got := -1
	d.writeIntensity = func(v int) error { got = v; return nil }

	ev := touch.Event{
		Kind: touch.KindSwipeRight,
		X:    render.SliderTrackX + render.SliderTrackW, Y: y,
		StartX: render.SliderTrackX, StartY: y,
	}
	h := render.HitTest(ev.StartX, ev.StartY, monthStart, true)
	if h.Kind != render.HitSettingsRow || h.Row != render.SettingsRowBrightness {
		t.Fatalf("测试前提不成立：落手点应命中背光行，实际 %v Row=%d", h.Kind, h.Row)
	}

	d.applyTouch(h, ev, today)

	if got != brightnessMax {
		t.Fatalf("从轨道左端拖到右端应写入 %d，实际 %d", brightnessMax, got)
	}
}

// 点击轨道按落手点跳档；落手点在行内但远离轨道也命中（整行 y 带）。
func TestApplyTouchBrightnessTapJumps(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	monthStart := ui.MonthStart(today, 0)
	// 轨道上方 40px，仍在行内 —— 用户从滑块上按下去时就是这种落点。
	y := render.SliderTrackY - 40

	d := newTestDeps(t, nil)
	d.uiState.SettingsOpen = true
	d.readIntensity = func() (int, error) { return 0, nil }
	got := -1
	d.writeIntensity = func(v int) error { got = v; return nil }

	x := render.SliderTrackX + render.SliderTrackW/2
	ev := touch.Event{Kind: touch.KindTap, X: x, Y: y, StartX: x, StartY: y}
	h := render.HitTest(ev.StartX, ev.StartY, monthStart, true)
	if h.Kind != render.HitSettingsRow || h.Row != render.SettingsRowBrightness {
		t.Fatalf("测试前提不成立：行内轨道外的落点应命中背光行，实际 %v Row=%d", h.Kind, h.Row)
	}

	d.applyTouch(h, ev, today)

	if got != brightnessMax/2 {
		t.Fatalf("点轨道中点应写入 %d，实际 %d", brightnessMax/2, got)
	}
}

// lipc 写失败：只记日志，不设 forceFullRefresh —— 下一帧读回真实值自动纠正。
func TestAdjustBrightnessWriteFailureOnlyLogs(t *testing.T) {
	var logs []string
	d := newTestDeps(t, nil)
	d.logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	d.readIntensity = func() (int, error) { return 12, nil }
	d.writeIntensity = func(int) error { return errors.New("lipc-set-prop rc=1") }

	d.applySettingsRow(render.SettingsRowBrightness, render.SliderTrackX+render.SliderTrackW)

	if d.forceFullRefresh {
		t.Fatal("写失败不该强制刷新 —— 界面上的旧值才是真实值")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "设置背光亮度") {
		t.Fatalf("写失败应记日志，实际 %v", logs)
	}
}

// lipc 读失败：不猜档位、不写 lipc，只记日志。
func TestAdjustBrightnessReadFailureDoesNothing(t *testing.T) {
	var logs []string
	d := newTestDeps(t, nil)
	d.logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	d.readIntensity = func() (int, error) { return 0, errors.New("lipc-get-prop: not found") }
	setCalled := false
	d.writeIntensity = func(int) error { setCalled = true; return nil }

	d.applySettingsRow(render.SettingsRowBrightness, render.SliderTrackX+render.SliderTrackW)

	if setCalled {
		t.Fatal("读不到当前档位时不该盲写 lipc")
	}
	if d.forceFullRefresh {
		t.Fatal("读失败不该强制刷新")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "读取背光亮度失败") {
		t.Fatalf("读失败应记日志，实际 %v", logs)
	}
}

// buildSnapshot 每帧读一次 lipc；读不到填 -1（界面显示「—」）。
//
// 变异：只在启动时读一次（把读取挪出 buildSnapshot）→ 调用计数停在 1。
func TestBuildSnapshotReadsBrightnessEveryFrame(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	d := newTestDeps(t, nil)
	calls := 0
	d.readIntensity = func() (int, error) { calls++; return 13, nil }

	snap := d.buildSnapshot(today)
	if snap.Info.Brightness != 13 || snap.Info.BrightnessMax != brightnessMax {
		t.Fatalf("Info 亮度 = %d / %d，期望 13 / %d",
			snap.Info.Brightness, snap.Info.BrightnessMax, brightnessMax)
	}
	if calls != 1 {
		t.Fatalf("buildSnapshot 应读一次 lipc，实际 %d 次", calls)
	}

	d.buildSnapshot(today)
	if calls != 2 {
		t.Fatalf("每一帧都应重新读 lipc，实际共 %d 次", calls)
	}

	d.readIntensity = func() (int, error) { return 0, errors.New("no lipc") }
	snap = d.buildSnapshot(today)
	if snap.Info.Brightness != -1 {
		t.Fatalf("读不到时 Brightness 应为 -1，实际 %d", snap.Info.Brightness)
	}
}

// 用假 initctl 脚本而不是只测 start/running：真机上出事的那一帧恰好是
// starting，只测 running 的话删掉这个分支测试仍然全绿。
func TestUpstartJobRunningRecognizesStarting(t *testing.T) {
	old := initctlPath
	defer func() { initctlPath = old }()

	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"start/running", "lab126_gui start/running, process 123", true},
		{"start/starting", "lab126_gui start/starting, process 123", true},
		{"stop/waiting", "lab126_gui stop/waiting", false},
		{"未知作业", "initctl: Unknown job: lab126_gui", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "initctl")
			body := "#!/bin/sh\n[ \"$1\" = status ] || exit 1\necho '" + c.out + "'\n"
			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			initctlPath = script
			if got := upstartJobRunning("lab126_gui"); got != c.want {
				t.Fatalf("upstartJobRunning() = %v，期望 %v（输出 %q）", got, c.want, c.out)
			}
		})
	}
}

// 启动时只有「标记存在」且「GUI 在跑」两条同时成立才让出。
//
// 变异：去掉 guiRunning 条件 → 「标记在但 GUI 没跑」也退出，时钟再也起不来；
// 去掉标记判断 → 用户用 KUAL 启动后仍会被立刻踢出。
func TestShouldYieldOnStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), yieldMarkerFile)
	guiUp := func() bool { return true }
	guiDown := func() bool { return false }

	if shouldYieldOnStart(marker, guiUp) {
		t.Fatal("标记不存在时不该让出")
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if shouldYieldOnStart(marker, guiDown) {
		t.Fatal("GUI 没在跑时不该让出 —— 独占模式的正常启动会先停 GUI")
	}
	if !shouldYieldOnStart(marker, guiUp) {
		t.Fatal("标记存在且 GUI 在跑时应让出")
	}
}

// 「立即全屏刷新」要给可见反馈：先清一次屏，再重绘设置页。
//
// 变异：只置 forceFullRefresh 而不清屏 → 用户看到的还是同一张设置页，
// 这一条会因为没有 -k 调用而红。
func TestManualFullRefreshFlashesThenRedraws(t *testing.T) {
	fbink, calls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	d.uiState.SettingsOpen = true

	d.applySettingsRow(render.SettingsRowRefresh, 0)
	if err := tickOnce(context.Background(), d, time.Date(2026, 10, 30, 14, 32, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	got := calls()
	if len(got) != 2 {
		t.Fatalf("应恰好两次 fbink 调用（清屏 + 重绘），实际 %d 次: %v", len(got), got)
	}
	if !strings.Contains(got[0], "-k") {
		t.Errorf("第一次应是清屏（-k），实际 %q", got[0])
	}
	if !strings.Contains(got[1], "file=") || strings.Contains(got[1], " -c ") {
		t.Errorf("第二次应是重绘且不再清屏，实际 %q", got[1])
	}
}

// 设置页开关的整屏绘制不能带 -c：整屏图像已盖满，清屏是多余的第二次
// 面板更新。这条走真实的 runLoop，确保不是只有 tickOnce 支持而已。
func TestSettingsToggleFullRefreshSkipsClear(t *testing.T) {
	fbink, calls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	ch := make(chan touch.Event, 1)
	d.touchCh = ch

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, d) }()

	// 点齿轮打开设置页。坐标直接落在齿轮热区内。
	ch <- touch.Event{
		Kind:   touch.KindTap,
		StartX: render.GearX + 10, StartY: render.GearY + 10,
		X: render.GearX + 10, Y: render.GearY + 10,
	}

	fullCall := ""
	waitFor(t, 3*time.Second, func() bool {
		for _, c := range calls() {
			if strings.Contains(c, "file=") && strings.Contains(c, "full.png") {
				fullCall = c
				return true
			}
		}
		return false
	}, "设置页打开后没有发生全屏重绘")

	if strings.Contains(fullCall, " -c ") {
		t.Fatalf("设置页开关的全屏绘制不该带 -c，实际 %q", fullCall)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runLoop 返回错误: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runLoop 没有在 ctx 取消后退出")
	}
}

// 点「返回 Kindle 界面」必须立刻退出主循环，不等下一轮 tick。
//
// 变异：applySettingsRow(0) 只启动 GUI、不置 yieldToGUI（旧实现）→
// runLoop 继续跑到下一分钟，本测试在 3 秒超时后红。
func TestReturnToGUIExitsRunLoopImmediately(t *testing.T) {
	old := startKindleGUI
	called := 0
	startKindleGUI = func() error { called++; return nil }
	defer func() { startKindleGUI = old }()

	d := newTestDeps(t, nil)
	d.uiState.SettingsOpen = true
	ch := make(chan touch.Event, 1)
	d.touchCh = ch

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, d) }()

	// 设置页第一行（「返回 Kindle 界面」）的中心。
	ch <- touch.Event{
		Kind:   touch.KindTap,
		StartX: 700, StartY: render.SettingsFirstRowY + render.SettingsRowH/2,
		X: 700, Y: render.SettingsFirstRowY + render.SettingsRowH/2,
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runLoop 返回错误: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("点「返回系统」后主循环没有立刻退出 —— 还会再画一轮表盘盖住 GUI")
	}
	if called != 1 {
		t.Fatalf("应启动原生界面 1 次，实际 %d 次", called)
	}
	if _, err := os.Stat(yieldMarkerPath(d.cfg.Display.WorkDir)); err != nil {
		t.Fatalf("退出前应留下让出标记: %v", err)
	}
}

func testPeriods() []config.Period {
	return []config.Period{
		{StartMin: 8 * 60, EndMin: 8*60 + 45},
		{StartMin: 8*60 + 55, EndMin: 9*60 + 40},
	}
}

// futureCourses 造 n 门都尚未结束的课，用于课程分页。
func futureCourses(now time.Time, n int) []state.Course {
	out := make([]state.Course, n)
	for i := range out {
		out[i] = state.Course{
			Name:  "课程" + strconv.Itoa(i+1),
			Start: now.Add(time.Duration(i) * time.Minute),
			End:   now.Add(2 * time.Hour),
		}
	}
	return out
}

// 触摸设备打不开时不能退出，也不能影响渲染。
func TestRunRealSurvivesTouchOpenFailure(t *testing.T) {
	old := openTouch
	openTouch = func(path, rotate string) (*touch.Reader, error) {
		return nil, errors.New("no such device")
	}
	defer func() { openTouch = old }()

	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)

	d := newTestDeps(t, nil)
	closer := attachTouch(d, "/nonexistent/event1", logger)
	if closer != nil {
		t.Fatal("触摸打不开时不应返回 close 函数")
	}
	if d.touchCh != nil {
		t.Fatal("触摸打不开时 touchCh 应保持 nil（交互禁用）")
	}
	if !strings.Contains(logs.String(), "触摸不可用") {
		t.Fatalf("应记录「触摸不可用」，日志为 %q", logs.String())
	}

	// 时钟照常：tick 能出帧。
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if err := tickOnce(context.Background(), d, now); err != nil {
		t.Fatalf("触摸失效不应影响 tick: %v", err)
	}
	if d.frameCount != 1 {
		t.Fatalf("应完成 1 帧，实际 %d", d.frameCount)
	}

	// 主循环也必须照常跑，直到 ctx 取消正常返回。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, d) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("触摸失效时 runLoop 应正常返回，得到 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runLoop 没有在 ctx 取消后退出")
	}
}

// 选中别的日期时，快照里的课程必须是那天的；今天仍走「尚未结束」逻辑。
func TestBuildSnapshotUsesSelectedDay(t *testing.T) {
	today := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)  // 周五
	selected := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) // 下周一，第 10 周

	d := newTestDeps(t, nil)
	d.cfg.Periods = testPeriods()
	// 整学期课表的锚点与「今日课表」不同：选中日分支必须显示 sched 自己的
	// 抓取时刻与学期名，不能混用今日课表的。
	schedFetched := today.Add(-24 * time.Hour) // 周四，所在周仍是 10-26 那周
	d.setSchedule(&state.Schedule{
		Week: 9, MaxWeek: 20, Semester: "整学期-秋季", FetchedAt: schedFetched,
		Specs: []state.CourseSpec{
			{Name: "周五的课", DayOfWeek: 5, StartSection: 1, EndSection: 2, Weeks: []int{9, 10}},
			{Name: "周一的课", DayOfWeek: 1, StartSection: 1, EndSection: 2, Weeks: []int{9, 10}},
		},
	})
	d.lastTimetable = &state.Timetable{
		Available: true, Semester: "今日-秋季", FetchedAt: today,
		Courses: []state.Course{{Name: "今天的课", Start: today, End: today.Add(time.Hour)}},
	}

	// 跟随今天：仍用今日课表（只显示尚未结束的课）。
	snap := d.buildSnapshot(today)
	if len(snap.Timetable.Courses) != 1 || snap.Timetable.Courses[0].Name != "今天的课" {
		t.Fatalf("跟随今天时应显示今日剩余课，得到 %+v", snap.Timetable.Courses)
	}
	if snap.Timetable.Semester != "今日-秋季" {
		t.Fatalf("跟随今天时应显示今日课表的学期名，得到 %q", snap.Timetable.Semester)
	}

	// 选中下周一：课程必须来自整学期课表里那一天，而不是今天的剩余课。
	d.uiState.SelectDate(selected, today)
	if d.uiState.FollowToday() {
		t.Fatal("选中别的日期后不应再跟随今天")
	}
	snap = d.buildSnapshot(today)
	if len(snap.Timetable.Courses) != 1 {
		t.Fatalf("选中日应恰好 1 门课，得到 %d 门: %+v",
			len(snap.Timetable.Courses), snap.Timetable.Courses)
	}
	if got := snap.Timetable.Courses[0].Name; got != "周一的课" {
		t.Fatalf("课程来源错误：应显示选中日的「周一的课」，得到 %q", got)
	}
	if snap.Timetable.Week != 10 {
		t.Fatalf("选中日的教学周应为 10，得到 %d", snap.Timetable.Week)
	}
	if snap.Timetable.Semester != "整学期-秋季" {
		t.Fatalf("选中日应显示整学期课表的学期名，得到 %q", snap.Timetable.Semester)
	}
	if !snap.Timetable.FetchedAt.Equal(schedFetched) {
		t.Fatalf("选中日应显示整学期课表的抓取时刻 %v，得到 %v",
			schedFetched, snap.Timetable.FetchedAt)
	}
	if snap.NextCourseIdx != -1 {
		t.Fatalf("选中别的日期时不应高亮「下一门课」，得到 %d", snap.NextCourseIdx)
	}

	// 整学期课表尚未就绪时，不能说「那天没有课」—— 那会把没数据伪装成确实无课。
	d.setSchedule(nil)
	snap = d.buildSnapshot(today)
	if snap.Timetable.Available {
		t.Fatalf("没有整学期课表时应标为不可用，得到 %+v", snap.Timetable)
	}
	if snap.Timetable.Message != "课表暂不可用" {
		t.Fatalf("Message = %q，期望「课表暂不可用」", snap.Timetable.Message)
	}
}

// 快照只装可见月份的 42 格所需数据，不把整年的日历塞进去。
func TestCalendarForMonthOnlyVisibleMonth(t *testing.T) {
	// 整年 365 天的日历。
	cal := make(map[string]state.DayInfo, 365)
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 365; i++ {
		cal[day.Format("2006-01-02")] = state.DayInfo{LunarDayName: "x"}
		day = day.AddDate(0, 0, 1)
	}
	cal["2026-11-07"] = state.DayInfo{SolarTerm: "立冬"}

	d := &deps{}
	d.mergeCalendar(2026, cal)

	monthStart := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	out := d.calendarForMonth(monthStart)
	// 只装本月（2026-11 有 30 天），补格与整年数据都不进来。
	if len(out) != 30 {
		t.Fatalf("应只装本月 30 天，实际 %d 天 —— 窗口过宽或塞了整年数据", len(out))
	}
	if _, ok := out["2026-11-07"]; !ok {
		t.Fatal("可见月份内的日期应保留")
	}
	for _, outside := range []string{
		"2026-10-31",                             // 上月补格
		"2026-12-01",                             // 下月补格
		"2026-01-15", "2026-06-01", "2026-12-31", // 其它月份
	} {
		if _, ok := out[outside]; ok {
			t.Fatalf("月份外的 %s 不该出现在快照里", outside)
		}
	}

	if got := (&deps{}).calendarForMonth(monthStart); got != nil {
		t.Fatalf("没有日历数据时应返回 nil，得到 %+v", got)
	}
}

// 设置页开关会改变整屏内容，下一帧必须全屏刷新。
func TestSettingsToggleForcesFullRefresh(t *testing.T) {
	fbink, calls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	ch := make(chan touch.Event, 2)
	d.touchCh = ch

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, d) }()

	// 点齿轮进设置页，再点「关闭」退出设置页（最后一行）。
	// 命中用落手点，所以 StartX/StartY 必须填成点击位置。
	// 两条事件按序入队：主循环必然先处理后处理，所以不必等第一条生效
	// 再发第二条（也不该并读 uiState —— 那是与主循环的数据竞争）。
	ch <- touch.Event{
		Kind: touch.KindTap,
		X:    render.GearX + 10, Y: render.GearY + 10,
		StartX: render.GearX + 10, StartY: render.GearY + 10,
	}
	closeY := render.SettingsFirstRowY + render.SettingsRowClose*render.SettingsRowH +
		render.SettingsRowH/2
	ch <- touch.Event{
		Kind: touch.KindTap,
		X:    render.CanvasW / 2, Y: closeY,
		StartX: render.CanvasW / 2, StartY: closeY,
	}

	// 开、关各需要一次全屏刷新。
	waitFor(t, 2*time.Second, func() bool { return countFullRefreshes(calls()) >= 2 },
		"设置页开关没有触发全屏刷新")
	cancel()
	<-done
	if d.uiState.SettingsOpen {
		t.Fatal("点击关闭后设置页应已关闭")
	}
}

// 60 秒无操作回位会改变整屏内容，下一帧必须全屏刷新。
func TestIdleResetForcesFullRefresh(t *testing.T) {
	fbink, calls := fakeFbink(t)
	d := newTestDeps(t, fbink)
	// 非默认态且已经超过 60 秒没有交互。
	d.uiState = ui.State{
		MonthOffset: 1,
		LastInput:   time.Now().Add(-idleReset - time.Second),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, d) }()

	waitFor(t, 2*time.Second, func() bool { return countFullRefreshes(calls()) >= 1 },
		"60 秒回位没有触发全屏刷新")
	cancel()
	<-done

	if !d.uiState.IsDefault() {
		t.Fatalf("回位后应回到默认态，得到 %+v", d.uiState)
	}
}

// 整学期课表的抓取线必须真的被 fetchLoop 驱动，并把结果写回 deps。
func TestFetchLoopFetchesSchedule(t *testing.T) {
	cfg := &config.Config{
		Time:    config.TimeConfig{UTCOffsetMinutes: 480},
		Weather: config.WeatherConfig{RefreshMinutes: 30},
		Timetable: config.TimetableConfig{
			Enabled:        true,
			RefreshMinutes: 10,
		},
		Display: config.DisplayConfig{WorkDir: t.TempDir()},
	}
	want := &state.Schedule{
		Week: 3, Semester: "秋季", FetchedAt: time.Now(),
		Specs: []state.CourseSpec{{Name: "某门课"}},
	}
	fetched := make(chan struct{}, 1)
	d := &deps{
		cfg: cfg,
		fetchWeather: func(context.Context, time.Time) (*state.Weather, error) {
			return nil, nil
		},
		fetchTimetable: func(context.Context, time.Time) (*state.Timetable, error) {
			return &state.Timetable{Available: true}, nil
		},
		fetchSchedule: func(context.Context, time.Time) (*state.Schedule, error) {
			select {
			case fetched <- struct{}{}:
			default:
			}
			return want, nil
		},
		logf:          func(string, ...any) {},
		lastTimetable: &state.Timetable{Available: false},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go fetchLoop(ctx, d, cfg, source.NewStore(t.TempDir()), log.New(io.Discard, "", 0))

	select {
	case <-fetched:
	case <-time.After(time.Second):
		t.Fatal("fetchLoop 没有调用 fetchSchedule")
	}
	waitFor(t, time.Second, func() bool { return d.getSchedule() != nil },
		"抓取成功后 lastSchedule 没有被写入")
	if got := d.getSchedule(); got == nil || len(got.Specs) != 1 {
		t.Fatalf("lastSchedule 内容不对: %+v", got)
	}
}
