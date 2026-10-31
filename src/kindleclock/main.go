// Command kindleclock 是设备上的主程序。
//
// 它把五个内部包装配成一条流水线：
//
//	fetch（后台 goroutine）→ state.Build → render → display
//
// 主循环按整分对齐，每分钟渲染一帧、只刷新变化的区域。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rhsy/kindleclock/src/internal/config"
	"github.com/rhsy/kindleclock/src/internal/display"
	"github.com/rhsy/kindleclock/src/internal/render"
	"github.com/rhsy/kindleclock/src/internal/source"
	"github.com/rhsy/kindleclock/src/internal/state"
	"github.com/rhsy/kindleclock/src/internal/touch"
	"github.com/rhsy/kindleclock/src/internal/ui"
)

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if opts.showVer {
		printVersion()
		return
	}

	// -toast 必须在拿 flock 之前分流：它只往屏幕上贴一张小图，
	// 不应该因为主循环正在跑就被拒绝。
	if opts.toastText != "" {
		if err := runToast(opts); err != nil {
			fmt.Fprintln(os.Stderr, "提示失败:", err)
			os.Exit(1)
		}
		return
	}

	if opts.previewTo != "" {
		if err := runPreview(opts); err != nil {
			fmt.Fprintln(os.Stderr, "预览失败:", err)
			os.Exit(1)
		}
		return
	}

	if err := runReal(opts); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

// ---- 预览模式（不接触设备） ----

func runPreview(o *options) error {
	snap, err := loadSnapshotFile(o.fixturesTo)
	if err != nil {
		return err
	}
	// 预览不需要真实配置：字体路径从配置读，失败则回退到 gofont
	fonts, fallback, err := loadPreviewFonts(o.configPath)
	if err != nil {
		return err
	}
	defer fonts.Close()
	if fallback {
		fmt.Fprintln(os.Stderr,
			"警告：配置文件不可用，已回退到内置 gofont —— 图中中文会显示为方框。"+
				"要看到真实版面，请用 -config 指向带中文字体的配置。")
	}

	r, err := render.NewRenderer(fonts)
	if err != nil {
		return err
	}
	img := r.Render(snap, ui.State{})
	return writePNG(img, o.previewTo)
}

// loadPreviewFonts 优先用配置里的字体；配置读不到就用内置 gofont，
// 让预览在没有配置文件的机器上也能跑。
//
// 第二个返回值表示是否走了回退路径 —— gofont 没有中文字形，中文会画成
// 方框，调用方必须把这件事说出来，否则「图出出来了」会被误当成装配正确。
func loadPreviewFonts(configPath string) (*render.Fonts, bool, error) {
	cfg, err := config.Load(configPath)
	if err == nil {
		f, err := render.NewFonts(cfg.Display.FontRegular, cfg.Display.FontBold)
		return f, false, err
	}
	f, err := render.NewFallbackFonts()
	return f, true, err
}

func loadSnapshotFile(path string) (state.Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return state.Snapshot{}, err
	}
	var snap state.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return state.Snapshot{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	return snap, nil
}

func writePNG(img image.Image, path string) error {
	// filepath.Dir("a.png") 返回 "."，此时不需要建目录。
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// ---- 真实运行 ----

// deps 是可注入的依赖集合。除了 main 之外，只有测试构造它。
type deps struct {
	cfg      *config.Config
	renderer *render.Renderer
	fbink    *display.Fbink
	differ   *display.Differ
	logf     func(format string, args ...any)

	fetchWeather   func(ctx context.Context, now time.Time) (*state.Weather, error)
	fetchTimetable func(ctx context.Context, now time.Time) (*state.Timetable, error)
	fetchSchedule  func(ctx context.Context, now time.Time) (*state.Schedule, error)
	fetchCalendar  func(ctx context.Context, year int) (map[string]state.DayInfo, error)

	// 抓取结果的最新快照，由后台 goroutine 写、主循环读
	mu            sync.RWMutex
	lastWeather   *state.Weather
	lastTimetable *state.Timetable
	// lastSchedule 是整学期课表，供「翻到别的日期」时裁剪那天的课。
	// 它与 lastTimetable（今天剩余课程）是两份数据：后者只够画今天。
	lastSchedule *state.Schedule

	// 交互状态与触摸事件源。touchCh 为 nil 时表示交互不可用（设备打不开），
	// 主循环退化成纯时钟。
	uiState ui.State
	touchCh <-chan touch.Event

	// forceFullRefresh 表示下一帧必须整屏重画。
	// 设置页开关与 60 秒回位都会改变整屏内容，局部刷新会留下大片残影。
	forceFullRefresh bool

	// fullRefreshNoClear 与 forceFullRefresh 搭配：整屏内容本来就是一张
	// 盖满画布的图，先 -c 清屏再画等于两次面板更新。设置页开关走这条；
	// 去残影仍由周期性的 fullRefresh（带 -c）负责。
	fullRefreshNoClear bool

	// flashFullRefresh 与 forceFullRefresh 搭配：先清一次屏再重绘，
	// 给「立即全屏刷新」一个看得见的反馈（用户点的是设置页本身，
	// 重画同一张图看不出任何变化）。
	flashFullRefresh bool

	// yieldToGUI 表示用户点了「返回 Kindle 界面」。runLoop 在本次触摸
	// 处理后立刻退出，不再等下一轮 tick 把表盘画回屏幕上。
	yieldToGUI bool

	// lastCalendar 是已加载的所有年份的并集。跨年时不能整表替换 ——
	// 那会让 12-31 当天的数据在元旦当天消失（而设备是 7×24 常驻的）。
	// calYears 记录哪些年份已经加载过，用于判断是否需要重抓。
	lastCalendar map[string]state.DayInfo
	calYears     map[int]bool

	frameCount int
	regions    []render.Region

	// displayFailures 是「连续」刷新失败的区域次数累计（一个区域失败算 1）。
	// 任何一轮全部成功就清零；到 guiRestoreThreshold 把原生界面还回去。
	displayFailures int

	// fullRefreshTried 表示本轮「连续失败」区间里已经试过一次全屏兜底。
	// 与 displayFailures 同生命周期：任一轮全成功、或达到 GUI 阈值交还设备
	// 时一起清零，下一轮失败区间可以重新试一次。
	fullRefreshTried bool

	// guiRunning 报告 Kindle 原生界面是否在跑。抽成字段是为了测试能替身。
	guiRunning func() bool

	// fbPath / fbSysDir 是自愈检测要读的 framebuffer 及其 sysfs 目录。
	// 抽成字段是为了测试能指到假文件上。
	fbPath     string
	fbSysDir   string
	batteryDir string // 电池 sysfs 目录；测试指向假目录

	// readIntensity / writeIntensity 是 lipc 背光接口。抽成字段是为了
	// 测试能替身，绝不真的在单测里执行 lipc-set-prop；nil 时用真实实现。
	readIntensity  func() (int, error)
	writeIntensity func(int) error

	// 手动刷新课表的瞬时状态。与 lastSchedule 共用 mu：
	// 后台 goroutine 写、主循环 buildSnapshot 读。
	refreshing            bool
	scheduleRefreshFailed bool
	// refreshDone 把「后台刷新完成」通知主循环，让它立刻重画一帧。
	// 缓冲 1：主循环没来得及消费时，重复完成通知只保留一条即可。
	refreshDone chan struct{}

	// lastHealFrame 是上一次自愈全屏重绘发生在第几帧，用于冷却。
	// 没有冷却的话，如果某人的设备上原生界面每分钟都在抢屏，
	// 我们会每分钟全屏闪一次 —— 那比被盖住还难受。
	lastHealFrame int
}

// localZone 按配置的 UTC 偏移造时区。
//
// 设备系统时区是 "GMT+8:0100"，不是 IANA 名，time.LoadLocation 会失败；
// 而设备上也没有 tzdata。直接用固定偏移，永远可用。
func localZone(cfg *config.Config) *time.Location {
	return time.FixedZone("local", cfg.Time.UTCOffsetMinutes*60)
}

// acquireLock 独占打开锁文件。拿不到就返回错误（已有实例在跑）。
//
// 拿到锁后 fd 必须一直开着 —— flock 随 fd 关闭而释放。
// 本函数不负责关闭，由调用方 defer。
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("已有实例在运行（%s 被锁）: %w", path, err)
	}
	return f, nil
}

func runReal(o *options) error {
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return err
	}

	// 系统时间错得离谱时，界面上的日期会整体错位且无从察觉。
	// 只记 ERROR、不修正：进程无权改系统时钟，静默改只会更糟。
	if year := time.Now().In(localZone(cfg)).Year(); year < 2024 || year > 2100 {
		fmt.Fprintf(os.Stderr, "警告：系统时间异常（年份 %d），时钟将按此显示\n", year)
	}

	fonts, err := render.NewFonts(cfg.Display.FontRegular, cfg.Display.FontBold)
	if err != nil {
		return err
	}
	defer fonts.Close()

	renderer, err := render.NewRenderer(fonts)
	if err != nil {
		return err
	}

	store := source.NewStore(cfg.Display.WorkDir)
	if err := os.MkdirAll(cfg.Display.WorkDir, 0o755); err != nil {
		return fmt.Errorf("创建工作目录: %w", err)
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)

	d := &deps{
		cfg:         cfg,
		renderer:    renderer,
		fbink:       display.NewFbink(cfg.Display.Fbink),
		differ:      display.NewDiffer(),
		logf:        logger.Printf,
		fbPath:      "/dev/fb0",
		fbSysDir:    "/sys/class/graphics/fb0",
		batteryDir:  "/sys/class/power_supply/max77696-battery",
		guiRunning:  func() bool { return upstartJobRunning("lab126_gui") },
		refreshDone: make(chan struct{}, 1),
	}

	// 上次用户点了「返回 Kindle 界面」：若标记还在且 GUI 确实在跑，
	// 说明这次启动只是 upstart 又被 GUI 事件拉起来的，别再抢屏。
	// KUAL 的 start.sh 会在用户主动「启动」时删掉标记。
	if shouldYieldOnStart(yieldMarkerPath(cfg.Display.WorkDir), d.guiRunning) {
		logger.Printf("检测到 %s 且原生界面在运行，按上次「返回 Kindle 界面」直接退出",
			yieldMarkerPath(cfg.Display.WorkDir))
		return nil
	}

	// 单实例保护：writeAtomic 用固定名 <name>.tmp，双实例会互相截断。
	// 拿到锁就持有到进程退出（fd 随进程关闭自动释放）。
	lockPath := filepath.Join(cfg.Display.WorkDir, "kindleclock.lock")
	lockFile, err := acquireLock(lockPath)
	if err != nil {
		return err
	}
	defer lockFile.Close()

	// 冷启动：先用磁盘上的 last-good 填上，界面立刻有内容
	if w, err := store.LoadWeather(); err == nil {
		d.lastWeather = w
		logger.Printf("恢复上次天气: %s %d° (抓于 %s)",
			w.Text, w.Temp, w.FetchedAt.Format("01-02 15:04"))
	} else {
		logger.Printf("天气缓存不可用（%v），按无缓存启动", err)
	}
	if tt, err := store.LoadTimetable(); err == nil {
		d.lastTimetable = tt
		logger.Printf("恢复上次课表: %d 门课 (抓于 %s)",
			len(tt.Courses), tt.FetchedAt.Format("01-02 15:04"))
	} else {
		logger.Printf("课表缓存不可用（%v），按无缓存启动", err)
		d.lastTimetable = initialTimetable(cfg)
	}
	// 整学期课表与「今天剩余课程」是两份缓存：没有它，翻到别的日期时
	// 课程区永远是空的。恢复失败只影响选中日期，不影响今天的显示。
	if sched, err := store.LoadSchedule(); err == nil {
		d.setSchedule(sched)
		logger.Printf("恢复整学期课表: %d 门课 (抓于 %s)",
			len(sched.Specs), sched.FetchedAt.Format("01-02 15:04"))
	} else {
		logger.Printf("整学期课表缓存不可用（%v），选中日期暂不能显示课程", err)
	}

	// 农历/节气/节假日：按年缓存。抓不到就不显示，不影响时钟。
	//
	// 除了当前年，若下一年的缓存已在磁盘上也一并加载：元旦刚过就重启时
	// 能立刻有数据，不用等第一次抓取。
	calYear := time.Now().In(localZone(cfg)).Year()
	for _, year := range []int{calYear, calYear + 1} {
		cal, err := store.LoadCalendar(year)
		if err != nil {
			if year == calYear {
				logger.Printf("节假日缓存不可用（%v），稍后重抓", err)
			}
			continue
		}
		d.mergeCalendar(year, cal)
		logger.Printf("恢复节假日缓存: %d 天（%d 年）", len(cal), year)
	}

	// 抓取器
	weatherFetcher := source.NewWeatherFetcher(
		"https://uapis.cn", cfg.Weather.City, nil)
	ttFetcher := source.NewTimetableFetcher(
		cfg.Timetable.BaseURL, cfg.Timetable.StudentID, cfg.Timetable.Password, nil)
	calFetcher := source.NewCalendarFetcher("https://uapis.cn", nil)
	if tok, err := store.LoadToken(); err == nil && tok != "" {
		ttFetcher.SetToken(tok)
	}

	d.fetchWeather = func(ctx context.Context, now time.Time) (*state.Weather, error) {
		if !cfg.Weather.Enabled {
			return nil, nil
		}
		w, err := weatherFetcher.Fetch(ctx, now)
		if err != nil {
			return nil, err
		}
		if err := store.SaveWeather(w); err != nil {
			logger.Printf("警告：保存天气失败: %v", err)
		}
		return w, nil
	}
	d.fetchTimetable = func(ctx context.Context, now time.Time) (*state.Timetable, error) {
		if !cfg.Timetable.Enabled {
			return nil, nil
		}
		tt, err := ttFetcher.Fetch(ctx, now)
		if err != nil {
			// spec §8：课表不可用时课程区要显示「课表暂不可用 + 原因码」。
			// 但 runFetch 只在 err == nil 时写 deps，光返回提示没人用；
			// 所以这里由 markTimetableUnavailable 顺手把界面状态推进过去。
			markTimetableUnavailable(d, err)
			return nil, err
		}
		if err := store.SaveTimetable(tt); err != nil {
			logger.Printf("警告：保存课表失败: %v", err)
		}
		// 只在成功时持久化 token：失败路径上的 token 要么已知无效，
		// 要么根本没换过。日志里永不打印 token 本身。
		if tok := ttFetcher.Token(); tok != "" {
			if err := store.SaveToken(tok); err != nil {
				logger.Printf("警告：保存 token 失败: %v", err)
			}
		}
		return tt, nil
	}
	d.fetchCalendar = func(ctx context.Context, year int) (map[string]state.DayInfo, error) {
		return calFetcher.FetchYear(ctx, year)
	}
	// 整学期课表：选中任意日期时课程区的数据来源。抓取频率按天（课表一学期
	// 基本不变），与「今日课程」每 10 分钟一条线分开。
	d.fetchSchedule = func(ctx context.Context, now time.Time) (*state.Schedule, error) {
		if !cfg.Timetable.Enabled {
			return nil, nil
		}
		sched, err := ttFetcher.FetchSchedule(ctx, now)
		if err != nil {
			return nil, err
		}
		if err := store.SaveSchedule(sched); err != nil {
			logger.Printf("警告：保存整学期课表失败: %v", err)
		}
		return sched, nil
	}

	// 阻止休眠
	if cfg.Display.PreventScreensaver {
		if err := exec.Command("lipc-set-prop", "com.lab126.powerd",
			"preventScreenSaver", "1").Run(); err != nil {
			logger.Printf("警告：无法阻止休眠（可能不是 Kindle 环境）: %v", err)
		}
	}
	// 可选：独占屏幕
	//
	// 先查状态再停：Job 已经停着时 `/sbin/stop` 会返回 1 并打印
	// "Job is already stopped"，那不是错误，但会在日志里留下一条
	// 让人以为出事了的警告。
	if cfg.Display.StopGUIOnStart && upstartJobRunning("lab126_gui") {
		if err := exec.Command("/sbin/stop", "lab126_gui").Run(); err != nil {
			logger.Printf("警告：停止 GUI 失败: %v", err)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	regions, err := display.MapRegions(render.Regions(), cfg.Display.Rotate)
	if err != nil {
		return err
	}
	d.regions = regions

	logger.Printf("启动完成：画布 %dx%d，旋转 %s，区域 %d 个",
		render.CanvasW, render.CanvasH, cfg.Display.Rotate, len(regions))

	// -once 模式：先同步抓一次，再画一帧。
	// 后台 fetchLoop 是异步的，若不等它就 tickOnce，画到的是冷启动
	// 恢复的旧数据（或什么都没有）。
	if o.once {
		onceCtx, cancelOnce := context.WithTimeout(ctx, 30*time.Second)
		defer cancelOnce()
		now := time.Now().In(localZone(cfg))
		if w, err := d.fetchWeather(onceCtx, now); err == nil && w != nil {
			d.setWeather(w)
		} else if err != nil {
			logger.Printf("-once：天气抓取失败，用缓存: %v", err)
		}
		if ttOnce, err := d.fetchTimetable(onceCtx, now); err == nil && ttOnce != nil {
			d.setTimetable(ttOnce)
		} else if err != nil {
			logger.Printf("-once：课表抓取失败，用缓存: %v", err)
		}
		// -once 同样是「从干净屏幕开始」的场景：局部刷新填不满那 4 条空白带，
		// 屏幕上原有的东西会留在那里。先全屏刷新一次，再由 tickOnce 补差分。
		if err := startupFullRefresh(onceCtx, d, now); err != nil {
			logger.Printf("-once：首帧全屏刷新失败: %v", err)
		}
		return tickOnce(onceCtx, d, now)
	}

	// 启动时强制全屏刷新一次。
	//
	// 为什么必须：六个区域只覆盖画布 72.5% 的像素，左栏有 4 条空白带
	// （y 0-71 / 470-539 / 868-961 / 1016-1071）。局部刷新
	// 永远不会碰它们，所以启动前屏幕上残留的任何内容（上一个程序、
	// 上一次运行的旧帧、GUI 弹窗）都会永久留在那里。
	// 实测：不清屏直接启动，上一次调试留下的文字一直挂在屏幕上。
	if err := startupFullRefresh(ctx, d, time.Now().In(localZone(cfg))); err != nil {
		logger.Printf("首帧全屏刷新失败: %v", err)
	}

	// 触摸交互：设备打不开时只记日志并禁用交互，绝不退出 ——
	// 时钟是这个设备的最低承诺，没有触摸也得照常走。
	if closer := attachTouch(d, "/dev/input/event1", logger); closer != nil {
		defer closer()
	}

	// 常驻模式：启动后台抓取循环
	go fetchLoop(ctx, d, cfg, store, logger)

	return runLoop(ctx, d)
}

// startupFullRefresh 用当前已知数据（冷启动的 last-good）画一帧并全屏刷新。
//
// 它同时把这一帧的指纹记进 differ：紧接着的第一次 tickOnce 若内容没变，
// 不会再局部刷新一遍。fbink 为 nil（测试/干跑）时直接返回。
func startupFullRefresh(ctx context.Context, d *deps, now time.Time) error {
	if d.fbink == nil {
		return nil
	}
	snap := d.buildSnapshot(now)

	landscape := d.renderer.Render(snap, d.uiState)
	portrait, err := display.Rotate(landscape, d.cfg.Display.Rotate)
	if err != nil {
		return fmt.Errorf("首帧旋转: %w", err)
	}
	if err := fullRefresh(ctx, d, portrait); err != nil {
		return err
	}
	// fullRefresh 会 Reset differ（下一帧全部视为脏）。首帧不该这样：
	// 紧接着同一分钟内的第一次 tickOnce 内容与这一帧逐像素相同，
	// 若全判脏就要把六个区域再刷一遍，屏幕上白闪六下。
	// 这里把本帧指纹记进去，让脏区比对从「屏幕当前内容」开始。
	d.differ.Dirty(portrait, d.regions)
	d.log("启动首帧已全屏刷新")
	return nil
}

// runLoop 是常驻主循环：每轮渲染一帧，然后睡到下一个整分。
//
// 除了定时器，还等触摸事件：有交互就立刻重绘一帧，不等下一个整分。
// 只有 ctx 取消才返回；单帧错误只记日志，绝不让循环退出 ——
// 时钟是这个设备的最低承诺。
func runLoop(ctx context.Context, d *deps) error {
	loc := localZone(d.cfg)
	for {
		now := time.Now().In(loc)
		// 回位会改变整屏内容（月份、选中高亮、设置页），必须强制全屏刷新。
		// 漏掉这一句的后果：用户翻到别的月份走开，第二天早上屏幕上是
		// 回位后的日期，但日历区仍是旧月份的残影。
		if d.uiState.ResetIfIdle(now, idleReset) {
			d.forceFullRefresh = true
		}
		if err := tickOnce(ctx, d, now); err != nil {
			if errors.Is(err, errYieldToGUI) {
				d.log("原生界面已启动，让出屏幕并退出")
				return nil
			}
			d.log("本轮渲染失败: %v", err)
		}
		next := nextMinute(now)
		select {
		case <-ctx.Done():
			d.log("收到退出信号，正在退出")
			return nil
		case <-time.After(time.Until(next)):
		case <-d.refreshDone:
			// 手动刷新课表结束：新数据/失败状态已经在 deps 里，
			// 整屏重画让设置页右侧和课程区立刻跟上，不用等下一个整分。
			d.forceFullRefresh = true
		case ev, ok := <-d.touchCh:
			if !ok {
				// 触摸设备没了（被拔掉/驱动退出）：退化成纯时钟，
				// 而不是拿着一个永远阻塞的 channel。
				d.touchCh = nil
				continue
			}
			now2 := time.Now().In(loc)
			monthStart := ui.MonthStart(now2, d.uiState.MonthOffset)
			wasOpen := d.uiState.SettingsOpen
			d.applyTouch(gestureHit(ev, monthStart, d.uiState.SettingsOpen), ev, now2)
			// 设置页是整屏覆盖，开关前后内容完全不同；局部刷新会留下
			// 表盘/设置页互相叠加的残影。
			//
			// 不走 -c：整张设置页/表盘已经盖满屏幕，清屏是多余的第二次
			// 面板更新（实测 ~0.5s → ~0.15s）。残影由周期性的全屏刷新清。
			if d.uiState.SettingsOpen != wasOpen {
				d.forceFullRefresh = true
				d.fullRefreshNoClear = true
			}
			// 用户点了「返回 Kindle 界面」：必须在这次触摸处理完后立刻退出，
			// 不能再走一轮 tick —— 那一轮会把表盘画回去，盖住刚起来的 GUI。
			if d.yieldToGUI {
				d.log("用户请求返回 Kindle 界面，让出屏幕并退出")
				return nil
			}
			// 立刻重绘一帧，不等下一个整分（继续循环，顶部会 tick）。
		}
	}
}

// idleReset 是无交互多久后回到「跟随今天」。
//
// 60 秒足够看完一屏内容，又不至于让人走开后永远停在别的月份。
const idleReset = 60 * time.Second

// todayIn 返回配置时区里 now 的日期 —— 与 HitTest 产出的日期同一时区，
// SelectDate 的「是不是今天」才不会被 UTC 偏移差一天。
func todayIn(cfg *config.Config, now time.Time) time.Time {
	t := now.In(localZone(cfg))
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// gestureHit 把一次手势翻译成命中目标。
//
// 必须用落手点（StartX/StartY），不能用抬指点：用户从课程区第一行向上滑
// 翻页时，抬手点会落进上方的日历网格 —— 按抬指点判定就会误选一个日期；
// 横滑抬手落进齿轮矩形还会误开设置页。方向判定仍在 touch 包里用起点算。
func gestureHit(ev touch.Event, monthStart time.Time, settingsOpen bool) render.Hit {
	return render.HitTest(ev.StartX, ev.StartY, monthStart, settingsOpen)
}

// applyTouch 把一次手势翻译成状态变化。
//
// 只改状态，不负责重绘 —— 调用方紧接着跑一次 tickOnce。
//
// 第一件事就是记 LastInput：漏掉它的话 60 秒回位永远不会触发，
// 用户翻到别的月份后会一直卡在那里。非点击手势也要记：用户正在操作，
// 不该被当作「无人值守」而回位。
func (d *deps) applyTouch(h render.Hit, ev touch.Event, now time.Time) {
	d.uiState.LastInput = now
	d.uiState.ResetIfIdle(now, idleReset)

	// 只有课程区与背光滑块接受滑动，其余目标只接受点击。
	//
	// 不这么判的话：设置页 Row 0 里一次竖直拖动就会执行「返回系统」，
	// 时钟直接退出；滑过日历格子会误选日期。这些动作都不该由滑动触发。
	sliderRow := h.Kind == render.HitSettingsRow && h.Row == render.SettingsRowBrightness
	if h.Kind != render.HitCourseArea && !sliderRow && ev.Kind != touch.KindTap {
		return
	}

	switch h.Kind {
	case render.HitCalendarPrevMonth:
		d.uiState.MonthOffset--
	case render.HitCalendarNextMonth:
		d.uiState.MonthOffset++
	case render.HitCalendarDay:
		if !h.Date.IsZero() {
			d.uiState.SelectDate(h.Date, todayIn(d.cfg, now))
		}
	case render.HitCourseArea:
		d.scrollCourses(ev, now)
	case render.HitSettingsGear:
		d.uiState.SettingsOpen = true
	case render.HitSettingsRow:
		// 点击：跳到落点对应的档位。拖动：按**抬手点**算。
		//
		// 电子墨水屏整屏刷新要 0.15~0.6 秒，拖动途中连续重绘会把屏幕糊成
		// 一片。touch 包每次手势只在抬手时产出一个事件，所以这里天然
		// 只更新一次 —— 不要在主循环里为拖动加中间帧。
		x := ev.StartX
		if sliderRow && ev.Kind != touch.KindTap {
			x = ev.X
		}
		d.applySettingsRow(h.Row, x)
	}
}

// scrollCourses 按手势翻课程页，并把 CourseScroll 夹紧在合法范围内。
//
// 上滑/左滑 = 下一页（内容向上走），下滑/右滑 = 上一页；
// 点击 = 下一页并在末页绕回首页 —— 电子墨水屏上点按比滑动可靠得多，
// 只留滑动的话用户可能找不到翻页方式。
//
// 夹紧在状态层做，而不是只依赖渲染层：越界的值会一直累积，
// 等课程数变少时用户会先看到几页空白才回到有效页。
func (d *deps) scrollCourses(ev touch.Event, now time.Time) {
	pages := d.coursePages(now)
	switch ev.Kind {
	case touch.KindSwipeUp, touch.KindSwipeLeft:
		d.uiState.CourseScroll++
	case touch.KindSwipeDown, touch.KindSwipeRight:
		d.uiState.CourseScroll--
	case touch.KindTap:
		d.uiState.CourseScroll++
		if d.uiState.CourseScroll >= pages {
			d.uiState.CourseScroll = 0
		}
	}
	if d.uiState.CourseScroll < 0 {
		d.uiState.CourseScroll = 0
	}
	if d.uiState.CourseScroll > pages-1 {
		d.uiState.CourseScroll = pages - 1
	}
}

// coursesForView 返回课程区在 now 这一帧应显示的课程。
//
// buildSnapshot 与 coursePages 共用这一条判据：页数必须由「实际画出来的
// 课程」算出。分成两份实现的话，哪天过滤规则变了，页数会与真实行数分叉，
// 表现为最后一页到不了或翻出空白页。
func (d *deps) coursesForView(now time.Time) []state.Course {
	if d.uiState.FollowToday() {
		// 与 buildSnapshot 的 state.Build 同一过滤：只保留尚未结束的课，
		// 数据不可用时返回空。
		return state.Build(now, nil, *d.getTimetable(), d.cfg.Periods, 0).Timetable.Courses
	}
	return state.CoursesForDay(d.getSchedule(), d.uiState.SelectedDate, d.cfg.Periods)
}

// coursePages 返回课程区当前应有几页。
func (d *deps) coursePages(now time.Time) int {
	n := len(d.coursesForView(now))
	pages := (n + render.CourseMaxRows - 1) / render.CourseMaxRows
	if pages < 1 {
		pages = 1
	}
	return pages
}

// applySettingsRow 分派设置页的六行的动作。
//
// Row 索引严格按 settingsRows 的顺序（render 包的唯一来源）：
// 0 返回系统 / 1 全屏刷新 / 2 背光亮度 / 3 刷新课表 /
// 4 设备信息（纯展示）/ 5 关闭。
// 顺序写错会让「关闭」触发调亮度或抓取，而且界面上看不出来。
//
// x 是落点横坐标，只有背光行用得到（滑块位置）。
func (d *deps) applySettingsRow(row, x int) {
	switch row {
	case render.SettingsRowBack: // 返回 Kindle 界面
		if err := startKindleGUI(); err != nil {
			d.log("启动原生界面失败: %v", err)
			return
		}
		// 标记「用户主动让出屏幕」。程序可能被 upstart 的
		// `start on started lab126_gui` 再拉起来一次；启动时看到标记
		// 且 GUI 在跑就应当直接退出，不要再把表盘画回去盖住 GUI。
		if err := os.WriteFile(yieldMarkerPath(d.cfg.Display.WorkDir), nil, 0o600); err != nil {
			d.log("写让出标记失败: %v（仍会立刻退出）", err)
		}
		// 不能等下一轮 tick：那一轮会先把表盘画到屏幕上，正好盖住
		// 刚开始显示的 GUI。runLoop 看到这个标记会立刻返回。
		d.yieldToGUI = true
	case render.SettingsRowRefresh: // 立即全屏刷新
		// 设置页正开着，重画的还是同一张图 —— 用户看不到任何变化。
		// 先清一次屏（全屏闪白）再重绘，给一个明确的「确实刷了」反馈。
		d.forceFullRefresh = true
		d.flashFullRefresh = true
	case render.SettingsRowBrightness:
		d.adjustBrightness(x)
	case render.SettingsRowRefreshSchedule:
		d.startScheduleRefresh()
	case render.SettingsRowInfo: // 设备信息 —— 纯展示，无动作
	case render.SettingsRowClose:
		d.uiState.SettingsOpen = false
	}
}

// brightnessMax 是 PW3 的 lipc 背光最大档位（设备实测 0..24）。
// lipc 只提供当前值，没有「上限」属性可读，所以写成常量。
const brightnessMax = 24

// brightnessLevelAt 把滑动条上的 x 换算成档位。
//
// 轨道左端 = 0、右端 = brightnessMax；四舍五入到最近的整数档。
// 轨道外的 x 按最近端夹紧：用户从滑块上按下去往外拖，抬手点常常出界。
func brightnessLevelAt(x int) int {
	dx := x - render.SliderTrackX
	if dx <= 0 {
		return 0
	}
	if dx >= render.SliderTrackW {
		return brightnessMax
	}
	// 加半个分母再整除 = 四舍五入，避免浮点，也保证不同机器结果一致。
	level := (dx*brightnessMax + render.SliderTrackW/2) / render.SliderTrackW
	if level < 0 {
		level = 0
	}
	if level > brightnessMax {
		level = brightnessMax
	}
	return level
}

// adjustBrightness 处理「背光亮度」行的点击 / 拖动：把 x 换算成档位并写入。
//
// 只有档位真的变化时才写 lipc、才强制刷新 —— 点在当前档位上不该白闪一次。
//
// 每次都现读 lipc 而不是拿上一帧快照里的值：设置页可能开着好几分钟，
// 期间别的程序（原生界面、KUAL）也可能改过亮度，按旧值算会误判「没变化」。
//
// 写失败只记日志、不碰任何显示状态：下一帧 buildSnapshot 会读回真实值，
// 界面自动纠正，不需要乐观地改一个没生效的数字。
func (d *deps) adjustBrightness(x int) {
	next := brightnessLevelAt(x)
	cur, err := d.getIntensity()
	if err != nil {
		d.log("读取背光亮度失败: %v", err)
		return
	}
	if next == cur {
		return
	}
	if err := d.setIntensity(next); err != nil {
		d.log("设置背光亮度 %d 失败: %v", next, err)
		return
	}
	// 滑块位置与数值画在设置页上；整屏重绘让新档位这一帧就出现。
	d.forceFullRefresh = true
}

// getIntensity 读 lipc 背光等级。readIntensity 非 nil 时用它（测试替身）。
func (d *deps) getIntensity() (int, error) {
	if d.readIntensity != nil {
		return d.readIntensity()
	}
	return lipcGetIntensity()
}

// setIntensity 写 lipc 背光等级。
func (d *deps) setIntensity(v int) error {
	if d.writeIntensity != nil {
		return d.writeIntensity(v)
	}
	return lipcSetIntensity(v)
}

// lipcGetIntensity 用 lipc 读背光等级。
//
// 用 lipc 而不是 sysfs：sysfs 的 brightness 是驱动原始值 0..4095，
// lipc 的 flIntensity 才是框架的档位，与用户看到的「第几档」一致。
func lipcGetIntensity() (int, error) {
	out, err := exec.Command("lipc-get-prop", "com.lab126.powerd", "flIntensity").Output()
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("解析 flIntensity %q: %w", s, err)
	}
	return v, nil
}

// lipcSetIntensity 用 lipc 写背光等级。
func lipcSetIntensity(v int) error {
	return exec.Command("lipc-set-prop", "com.lab126.powerd", "flIntensity",
		strconv.Itoa(v)).Run()
}

// scheduleRefreshTimeout 是手动刷新整学期课表的超时。
// 抽成变量：测试可以缩短它来验证超时路径，不用真等 20 秒。
var scheduleRefreshTimeout = 20 * time.Second

// startScheduleRefresh 在后台刷新课表的两条线。
//
// 必须异步：抓取要几秒，阻塞主循环会让时钟停摆。状态通过 mutex 写进 deps，
// buildSnapshot 再带进快照；渲染层完全不知道 goroutine 的存在。
//
// 为什么两条都刷：Schedule 管「翻到别的日期时显示那天的课」，Timetable 管
// 「今天还剩什么课」—— 用户当下看的就是后者。只刷前者的话，点完屏幕上
// 什么都不会变，用户会以为按钮坏了。
//
// 已经在刷新时再点直接忽略 —— 否则连点五下会起五个 goroutine 抢着写缓存。
// 任一请求失败都算整体失败，但两条线各自的旧数据都保留。
func (d *deps) startScheduleRefresh() {
	d.mu.Lock()
	if d.refreshing {
		d.mu.Unlock()
		return
	}
	d.refreshing = true
	d.scheduleRefreshFailed = false
	d.mu.Unlock()

	// 本次触摸处理完之后 runLoop 会立刻 tick，让「刷新中…」马上出现。
	d.forceFullRefresh = true

	if d.fetchSchedule == nil || d.fetchTimetable == nil {
		d.log("手动刷新课表失败: 没有抓取器")
		d.finishScheduleRefresh(false)
		return
	}

	now := time.Now()
	if d.cfg != nil {
		now = now.In(localZone(d.cfg))
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), scheduleRefreshTimeout)
		defer cancel()

		failed := false

		// 1. 整学期课表：翻到别的日期时用。
		sched, err := d.fetchSchedule(ctx, now)
		if err != nil {
			failed = true
			d.log("手动刷新课表失败（整学期）: %v", err)
		} else if sched != nil {
			d.setSchedule(sched)
		}

		// 2. 今日课表（dashboard）：用户当下看到的那块。
		tt, err := d.fetchTimetable(ctx, now)
		if err != nil {
			failed = true
			d.log("手动刷新课表失败（今日）: %v", err)
		} else if tt != nil {
			d.setTimetable(tt)
		}

		d.finishScheduleRefresh(!failed)
	}()
}

// finishScheduleRefresh 收尾一次手动刷新，并叫醒主循环重画一帧。
//
// 这里不直接写 d.forceFullRefresh：那是主循环的字段，后台 goroutine 写会
// 形成数据竞争。发一条通知，由 runLoop 在自己的 goroutine 里置位。
func (d *deps) finishScheduleRefresh(ok bool) {
	d.mu.Lock()
	d.refreshing = false
	d.scheduleRefreshFailed = !ok
	d.mu.Unlock()

	if d.refreshDone != nil {
		select {
		case d.refreshDone <- struct{}{}:
		default:
			// 已有未处理的完成通知，丢掉这条也不会漏状态 ——
			// 下一帧读的是 deps 的最终值。
		}
	}
}

func (d *deps) scheduleRefreshing() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.refreshing
}

func (d *deps) didScheduleRefreshFail() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.scheduleRefreshFailed
}

// startKindleGUI 是变量而非直接调用，测试才能替身，避免在单测里真的
// 启动设备上的原生界面。
var startKindleGUI = func() error {
	return exec.Command("/sbin/start", "lab126_gui").Run()
}

// openTouch 是变量而非直接调用 touch.Open，测试才能模拟「设备打不开」。
var openTouch = touch.Open

// attachTouch 打开触摸设备并把它接到主循环上。
//
// 失败时只记日志、让 touchCh 保持 nil（交互禁用），绝不返回错误或退出 ——
// 没有触摸的时钟仍然完美履行它的职责。返回的 close 函数用于进程退出时
// 释放设备；失败时返回 nil。
func attachTouch(d *deps, path string, logger *log.Logger) func() {
	r, err := openTouch(path, d.cfg.Display.Rotate)
	if err != nil {
		logger.Printf("触摸不可用（%v），交互功能禁用，时钟照常运行", err)
		return nil
	}
	d.touchCh = r.Events()
	return func() {
		if err := r.Close(); err != nil {
			logger.Printf("关闭触摸设备失败: %v", err)
		}
	}
}

// nextMinute 返回严格晚于 now 的下一个整分。
//
// 每轮重新计算而不是「上次 + 1 分钟」：后者会把处理耗时的误差累积下去。
func nextMinute(now time.Time) time.Time {
	return now.Truncate(time.Minute).Add(time.Minute)
}

// errYieldToGUI 表示「原生界面被重新启动了，我们该让出屏幕」。
//
// 独立占屏模式下这是正常收尾，不是错误：用户用 SSH 跑了
// /sbin/start lab126_gui，说明他要看书，我们就该退出，而不是和它抢屏 ——
// 抢的结果是自愈每 5 分钟把表盘盖回他的书上。
var errYieldToGUI = errors.New("原生界面已启动，让出屏幕")

// buildSnapshot 组装这一帧要显示的全部数据。
//
// tickOnce 与 startupFullRefresh 必须用同一份组装逻辑 —— 否则首帧和
// 后续帧的内容会不一致，脏区比对会在开机后立刻多刷一次。
func (d *deps) buildSnapshot(now time.Time) state.Snapshot {
	// 天气与课表各只取一次：后台抓取可能随时更新它们，分两次取会让
	// 表盘内容与设置页的信息行来自不同版本的数据。
	w := d.getWeather()
	tt := d.getTimetable()
	snap := state.Build(now, w, *tt, d.cfg.Periods,
		time.Duration(d.cfg.Weather.StaleAfterMinutes)*time.Minute)

	// 选中了别的日期时，课程区显示那一天的课（全天，不过滤已结束）。
	//
	// 今天仍走 state.Build 的「只显示尚未结束」逻辑 —— 那是这块屏幕
	// 最重要的信息，不能因为加了「翻到某天」而退化。
	if !d.uiState.FollowToday() {
		day := d.uiState.SelectedDate
		sched := d.getSchedule()
		if sched == nil || sched.FetchedAt.IsZero() {
			// 整学期课表还没就绪（首次启动、抓取尚未成功）。此时说
			// 「那天没有课」是撒谎 —— 没数据与确实无课必须分开。
			snap.Timetable = state.Timetable{
				Available: false,
				Message:   "课表暂不可用",
				FetchedAt: tt.FetchedAt,
			}
		} else {
			courses := d.coursesForView(now)
			msg := ""
			if len(courses) == 0 {
				msg = "那天没有课"
			}
			snap.Timetable = state.Timetable{
				Week:      state.WeekOf(sched, day),
				Semester:  sched.Semester,
				Courses:   courses,
				Message:   msg,
				Available: true,
				FetchedAt: sched.FetchedAt,
			}
		}
		snap.NextCourseIdx = -1
	}

	snap.Day = d.dayInfo(now)
	// 日历要标出可见月份里每一天的节假日，所以只取可见月份的数据。
	// 注意：Day 仍取「今天」—— 左栏的日期/农历是「此刻」，不随选中日变。
	snap.Calendar = d.calendarForMonth(ui.MonthStart(now, d.uiState.MonthOffset))
	if d.cfg.Display.ShowBattery && d.batteryDir != "" {
		snap.Battery = readBattery(d.batteryDir)
	}
	// 设置页「设备信息」的全部内容。读文件/环境变量的活儿留在这里，
	// 渲染层只消费快照 —— 它必须保持纯函数。
	//
	// 背光每次组装都现读一次：设置页开着时用户可能刚调过档位，只有读回
	// 真实值才能把新数字画出来。读不到填 -1，渲染层显示「—」。
	brightness, err := d.getIntensity()
	if err != nil {
		brightness = -1
	}
	snap.Info = state.SystemInfo{
		ProgramVersion: buildVersion,
		Firmware:       readFirmware(),
		Week:           snap.Timetable.Week,
		WeatherAt:      weatherFetchedAt(w),
		TimetableAt:    tt.FetchedAt,
		Brightness:     brightness,
		BrightnessMax:  brightnessMax,

		Refreshing:            d.scheduleRefreshing(),
		ScheduleAt:            scheduleFetchedAt(d.getSchedule()),
		ScheduleRefreshFailed: d.didScheduleRefreshFail(),
	}
	return snap
}

// firmwarePath 是 Kindle 的固件版本文件。抽成变量便于测试指向假文件；
// 必须在第一次调用 readFirmware 之前设置（结果会被缓存）。
var firmwarePath = "/etc/version.txt"

var (
	firmwareOnce sync.Once
	firmwareVer  string
)

// readFirmware 读设备固件版本。进程内只读一次：固件版本运行时不会变，
// 而 buildSnapshot 每帧都会调用它。读不到时返回空串 —— 设置页少显示
// 一块，不能因此报错。
func readFirmware() string {
	firmwareOnce.Do(func() {
		b, err := os.ReadFile(firmwarePath)
		if err != nil {
			return
		}
		firmwareVer = strings.TrimSpace(string(b))
	})
	return firmwareVer
}

// weatherFetchedAt 返回天气的抓取时刻；从未抓到过时返回零值（界面显示「无」）。
func weatherFetchedAt(w *state.Weather) time.Time {
	if w == nil {
		return time.Time{}
	}
	return w.FetchedAt
}

// scheduleFetchedAt 返回整学期课表的抓取时刻；还没有课表时返回零值
// （设置页「刷新课表」行不显示「上次 HH:MM」）。
func scheduleFetchedAt(s *state.Schedule) time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.FetchedAt
}

// tickOnce 渲染并刷新一帧。错误只记录不返回（时钟必须继续走），
// 唯一的例外是 errYieldToGUI —— 那要终止主循环。
func tickOnce(ctx context.Context, d *deps, now time.Time) error {
	// 独占模式下，如果原生界面又被拉起来了，就交还屏幕并退出。
	// upstart 的「正常退出」不会 respawn，所以不会被立刻拉回来。
	if d.cfg.Display.StopGUIOnStart && d.guiRunning != nil && d.guiRunning() {
		return errYieldToGUI
	}

	snap := d.buildSnapshot(now)

	landscape := d.renderer.Render(snap, d.uiState)
	portrait, err := display.Rotate(landscape, d.cfg.Display.Rotate)
	if err != nil {
		return err
	}

	d.frameCount++

	// 设置页开关 / 60 秒回位后的整屏重画。必须走 fullRefresh：
	// 这两类变化会改动整个画布（表盘与设置页互相覆盖、月份与选中高亮
	// 同时变化），局部刷新只覆盖六个区域，会在空白带留下大片残影。
	if d.forceFullRefresh && d.fbink != nil {
		d.forceFullRefresh = false
		flash, noClear := d.flashFullRefresh, d.fullRefreshNoClear
		d.flashFullRefresh, d.fullRefreshNoClear = false, false
		if flash {
			// 「立即全屏刷新」：先清一次屏制造可见的闪烁，再重绘当前页。
			// 清屏失败也继续重绘 —— 重绘本身可能照样成功，不能因为
			// 反馈没闪就不把页面补回来。
			if err := d.fbink.ClearFull(ctx); err != nil {
				d.log("全屏刷新反馈清屏失败: %v", err)
			}
			return fullRefreshNoClear(ctx, d, portrait)
		}
		if noClear {
			return fullRefreshNoClear(ctx, d, portrait)
		}
		return fullRefresh(ctx, d, portrait)
	}

	// 周期性全屏刷新去残影
	if d.cfg.Display.FullRefreshEvery > 0 &&
		d.frameCount%d.cfg.Display.FullRefreshEvery == 0 {
		if d.fbink == nil {
			return nil
		}
		return fullRefresh(ctx, d, portrait)
	}

	// 自愈：核对屏幕上的是不是我们画的东西。
	//
	// 电子墨水屏上「我们画了什么」和「屏幕显示什么」是两回事。Kindle 的原生
	// 界面、KUAL 的提示、框架重启都能把我们的画面盖掉；而脏区比对只知道自己
	// 上一帧画了什么，被盖之后它认为一切正常 —— 结果只有时钟区还在更新，
	// 其余部分永远停在别人的画面上。真机上就是这么表现的：手指点一下屏幕，
	// 主页盖上来，之后表盘只剩时间还在走。
	// lastHealFrame == 0 表示从未自愈过（frameCount 从 1 开始），
	// 否则第一帧会被冷却期挡掉。
	if d.cfg.Display.VerifyScreen && d.fbink != nil &&
		(d.lastHealFrame == 0 || d.frameCount-d.lastHealFrame >= healCooldownFrames) {
		if d.screenClobbered(portrait) {
			d.lastHealFrame = d.frameCount
			d.log("屏幕内容与预期不符（被覆盖），全屏重绘")
			return fullRefresh(ctx, d, portrait)
		}
	}

	dirty := d.differ.Dirty(portrait, d.regions)
	if len(dirty) == 0 {
		return nil
	}

	// fbink 为 nil 时只渲染不显示（测试与干跑用）
	if d.fbink == nil {
		return nil
	}

	var failed int
	for _, r := range dirty {
		p := filepath.Join(d.cfg.Display.WorkDir, "frame-"+r.ID+".png")
		if err := display.WriteRegionPNG(portrait, r.Rect, p); err != nil {
			d.log("写 %s 失败: %v", r.ID, err)
			failed++
			continue
		}
		if err := d.fbink.DrawRegion(ctx, p, r.Rect, r.Waveform); err != nil {
			d.log("刷新 %s 失败: %v", r.ID, err)
			failed++
		}
	}

	// 只要有区域刷失败，就丢掉这一帧的指纹。
	//
	// differ.Dirty 在比对时就把本帧指纹写进了 d.last，而 DrawRegion 之后
	// 才可能失败。不清掉的话 differ 认为「这块已经是最新的」，只要该区域
	// 内容不变（日历、天气、课程、日期、footer —— 除时钟外的全部区域），
	// 就永远不会重试，屏幕永久停在旧内容。
	// spec §5.5：全屏刷新条件包含「任意一次区域更新失败」。
	if failed > 0 {
		d.differ.Reset()
		d.displayFailures += failed
		d.log("本轮 %d 个区域刷新失败（累计 %d 次）", failed, d.displayFailures)

		// 中间档：连续失败到 2 次时试一次全屏，看能不能救回来。
		//
		// 为什么不是 1 次：单次瞬时失败（fbink 被信号打断、EPDC 队列满）
		// 下一帧局部重试就能过去，为它付一次全屏 GC16 的闪烁不值得 ——
		// 这是对 spec §5.5 字面「任意一次失败就全屏」的有意偏离。
		//
		// 为什么要有 2 次这档：失败若是持续但局部的（某个区域裁切错了、
		// 某张 PNG 写坏了），只重试的话那个区域会静默停在旧内容上，
		// 直到第 10 次把 GUI 整个还回去 —— 中间缺一次「整屏重画试试」。
		//
		// 整个失败区间**只试一次**：全屏救得回来，下一帧成功就自然清零；
		// 救不回来（fbink 整个坏了、EPDC 堵死），第 2 次和第 8 次全屏是
		// 一样的结果，多付的整屏 GC16 闪烁纯属白给。
		//
		// 全屏本身也可能失败：失败不计入 displayFailures，只记日志，
		// 让 10 次阈值继续把 GUI 还回去。
		if d.displayFailures >= displayRetryFullRefreshAt && !d.fullRefreshTried {
			d.fullRefreshTried = true
			if err := fullRefresh(ctx, d, portrait); err != nil {
				d.log("全屏刷新也失败: %v（继续等待下一帧）", err)
			}
		}

		if d.displayFailures >= guiRestoreThreshold {
			d.log("显示连续失败 %d 次，把原生界面还回去", d.displayFailures)
			if err := restoreGUI(); err != nil {
				d.log("恢复 GUI 失败: %v", err)
			}
			// 设备已交还，下一次进来应是全新的一轮
			d.displayFailures = 0
			d.fullRefreshTried = false
		}
		return nil
	}
	d.displayFailures = 0
	d.fullRefreshTried = false
	return nil
}

// guiRestoreThreshold 是连续失败多少次后把原生界面还回去（spec §5.6）。
const guiRestoreThreshold = 10

// 自愈相关的常量。
const (
	// healCooldownFrames 是两次自愈全屏重绘之间至少隔多少帧。
	// 5 帧 = 5 分钟。没有冷却的话，如果原生界面每分钟都抢屏，
	// 我们会每分钟全屏闪一次 —— 比被盖住还难受。
	healCooldownFrames = 5

	// healSampleStep 是比对屏幕时的采样步长（每 N 个像素取一个）。
	// 盖屏是成片的，稀疏采样足够；逐点比 150 万个点没必要。
	healSampleStep = 4

	// healThreshold 是「我们画的笔画」消失的比例阈值。
	// 原生界面的状态栏只压住右栏最右 7 列，实测占比很低；
	// 而一旦别人的界面成片盖上来，我们的笔画会大面积消失。
	// 35% 的意思是「三分之一以上的笔画不在了」才算被盖 ——
	// 宁可漏判（下一分钟还会再查一次）也不要误判（会无缘无故闪屏）。
	healThreshold = 0.35
)

// screenClobbered 读一次当前画面，判断它是否还是我们画的那张。
//
// 读不到 framebuffer 时返回 false：这是自愈机制，读不到就不自愈，
// 绝不能因为检测失败反而把好好的屏幕重画一遍。
func (d *deps) screenClobbered(want *image.Gray) bool {
	screen, err := display.ReadVisibleFrame(d.fbPath, d.fbSysDir)
	if err != nil {
		d.log("读取屏幕内容失败（跳过自愈检测）: %v", err)
		return false
	}
	ratio := display.InkMismatchRatio(screen, want, healSampleStep)
	if ratio >= healThreshold {
		d.log("屏幕上有 %.1f%% 的笔画不见了", ratio*100)
		return true
	}
	return false
}

// displayRetryFullRefreshAt 是连续失败到多少次时先试一次全屏刷新。
//
// 介于「下一帧局部重试」与「把 GUI 还回去」之间的中间档：
// 应对持续但局部的失败（区域裁切错、PNG 写坏），否则它会在旧内容上
// 静默停 10 分钟才等到 GUI 恢复。详见 tickOnce 里的说明。
const displayRetryFullRefreshAt = 2

// restoreGUI 是变量而非直接调用，测试才能替换掉它。
var restoreGUI = func() error {
	return exec.Command("/sbin/start", "lab126_gui").Run()
}

// fullRefresh 全屏重绘一次，并重置 differ（下一帧全部视为脏）。
func fullRefresh(ctx context.Context, d *deps, portrait *image.Gray) error {
	return drawFull(ctx, d, portrait, true)
}

// fullRefreshNoClear 与 fullRefresh 相同，只是不先清屏（省一次面板更新）。
func fullRefreshNoClear(ctx context.Context, d *deps, portrait *image.Gray) error {
	return drawFull(ctx, d, portrait, false)
}

func drawFull(ctx context.Context, d *deps, portrait *image.Gray, clear bool) error {
	p := filepath.Join(d.cfg.Display.WorkDir, "full.png")
	// 竖版尺寸是 1072×1448，即宽 = CanvasH、高 = CanvasW（横版是 1448×1072）
	if err := display.WriteRegionPNG(portrait,
		render.Rect{X: 0, Y: 0, W: render.CanvasH, H: render.CanvasW}, p); err != nil {
		return err
	}
	var err error
	if clear {
		err = d.fbink.DrawFull(ctx, p)
	} else {
		err = d.fbink.DrawFullNoClear(ctx, p)
	}
	if err != nil {
		return err
	}
	d.differ.Reset()
	d.log("已全屏刷新（第 %d 帧）", d.frameCount)
	return nil
}

func (d *deps) getWeather() *state.Weather {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastWeather
}

func (d *deps) getTimetable() *state.Timetable {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.lastTimetable == nil {
		return initialTimetable(d.cfg)
	}
	return d.lastTimetable
}

// getSchedule 返回整学期课表；尚未加载/抓取成功时返回 nil。
func (d *deps) getSchedule() *state.Schedule {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastSchedule
}

func (d *deps) setSchedule(s *state.Schedule) {
	d.mu.Lock()
	d.lastSchedule = s
	// 拿到新数据说明抓取链路已经恢复：不要让上一次手动刷新的「失败」
	// 一直挂在屏幕上（自动抓取成功也算恢复）。
	d.scheduleRefreshFailed = false
	d.mu.Unlock()
}

// initialTimetable 是没有课表数据时显示的状态。
//
// 关掉课表（config 里 timetable.enabled = false）和「还没抓到第一份」是两回事，
// 界面不该说同一句话：前者永远不会有数据，写「课表同步中」会让人一直等；
// 后者是同步中的正常过渡态。
func initialTimetable(cfg *config.Config) *state.Timetable {
	if cfg != nil && !cfg.Timetable.Enabled {
		return &state.Timetable{Available: false, Message: "课表未启用"}
	}
	return &state.Timetable{Available: false, Message: "课表同步中"}
}

func (d *deps) setWeather(w *state.Weather) {
	d.mu.Lock()
	d.lastWeather = w
	d.mu.Unlock()
}

// readBattery 读 sysfs 里的电量。读不到就返回 Known=false —— 界面据此不画，
// 而不是显示一个 0% 骗人。
func readBattery(dir string) state.Battery {
	capB, err := os.ReadFile(filepath.Join(dir, "capacity"))
	if err != nil {
		return state.Battery{}
	}
	pct, err := strconv.Atoi(strings.TrimSpace(string(capB)))
	if err != nil || pct < 0 || pct > 100 {
		return state.Battery{}
	}
	st, _ := os.ReadFile(filepath.Join(dir, "status"))
	return state.Battery{
		Pct:      pct,
		Charging: strings.TrimSpace(string(st)) == "Charging",
		Known:    true,
	}
}

// dayInfo 返回某一天的农历/节气/节假日；没有数据时返回零值（界面不画）。
func (d *deps) dayInfo(t time.Time) state.DayInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.lastCalendar == nil {
		return state.DayInfo{}
	}
	return d.lastCalendar[t.Format("2006-01-02")]
}

// calendarForMonth 挑出可见月份的逐日信息，供日历标记节假日。
//
// 只装本月，不装整年，也不装相邻月份的补格：渲染层只对本月的格子查
// Snapshot.Calendar（补格不参与任何装饰，见 render.drawCalendar）。快照每帧
// 都重建，整年 map 会让每帧多一次 365 条的拷贝，而实际只需要 28~31 条。
func (d *deps) calendarForMonth(monthStart time.Time) map[string]state.DayInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.lastCalendar == nil {
		return nil
	}
	first := time.Date(monthStart.Year(), monthStart.Month(), 1,
		0, 0, 0, 0, monthStart.Location())
	next := first.AddDate(0, 1, 0)
	out := make(map[string]state.DayInfo, next.AddDate(0, 0, -1).Day())
	for day := first; day.Before(next); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		if v, ok := d.lastCalendar[key]; ok {
			out[key] = v
		}
	}
	return out
}

// mergeCalendar 把某一年并进已有表，并记下这一年已加载。
//
// 不能整表替换：跨年时旧年份的数据还要用（用户可能翻回去看），
// 而且替换会让 12-31 的数据在元旦当天凭空消失。
func (d *deps) mergeCalendar(year int, cal map[string]state.DayInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastCalendar == nil {
		d.lastCalendar = make(map[string]state.DayInfo, len(cal))
	}
	for k, v := range cal {
		d.lastCalendar[k] = v
	}
	if d.calYears == nil {
		d.calYears = make(map[int]bool, 2)
	}
	d.calYears[year] = true
}

// hasCalendarYear 报告某一年是否已加载。
func (d *deps) hasCalendarYear(year int) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.calYears[year]
}

// markTimetableUnavailable 把课程区推进到「不可用」状态。
//
// 只在手上没有可显示的数据时推进（`!prev.Available`）；有 last-good 时
// 原样保留 —— 这是 spec §8「502 保留 last-good」与「401 显示不可用」
// 两条规则的统一判据。
//
// cause 会以截断后的原因码追加进 Message（spec §8 要求「课表暂不可用 + 原因码」）。
// tFetcher 的错误串已经过 %.200q 截断、不含凭据，可以放到界面上。
func markTimetableUnavailable(d *deps, cause error) {
	prev := d.getTimetable()
	msg := "课表暂不可用"
	if cause != nil {
		msg += "：" + shortErr(cause)
	}
	if !prev.Available {
		d.setTimetable(&state.Timetable{
			Available: false,
			Message:   msg,
			FetchedAt: prev.FetchedAt, // 界面据此显示「上次成功 HH:MM」
		})
	}
}

// shortErr 把错误压成能塞进课程区一行的短语。
//
// 按 rune 截断而不是字节：错误里可能有中文，按字节切会切出半个字，
// 渲染层拿到非法 UTF-8 会画出乱码。
func shortErr(err error) string {
	const maxRunes = 40
	s := err.Error()
	r := []rune(s)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}

func (d *deps) setTimetable(t *state.Timetable) {
	d.mu.Lock()
	d.lastTimetable = t
	// 同 setSchedule：自动抓取成功后清掉上一次手动刷新的失败标记，
	// 否则后台已经恢复了，屏幕上还挂着「失败」。
	d.scheduleRefreshFailed = false
	d.mu.Unlock()
}

// log 统一走 logf，nil 时退回标准库 —— 测试构造的 deps 可能不设 logf，
// 日志绝不能因为少了一个字段就 panic。
func (d *deps) log(format string, args ...any) {
	if d.logf != nil {
		d.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// fetchLoop 周期性抓取天气、课表与节假日。**永不退出**，除非 ctx 取消。
func fetchLoop(ctx context.Context, d *deps, cfg *config.Config, store *source.Store, logger *log.Logger) {
	// 抓取侧必须用配置时区的 now：source.toCourse 用传入 now 的 Location()
	// 拼课程绝对时间。若这里用 time.Now()（系统本地时区，设备上可能是
	// 回落成 UTC 的 time.Local），而显示侧用 localZone，课程会整体偏 8 小时。
	now := func() time.Time { return time.Now().In(localZone(cfg)) }
	weatherEvery := time.Duration(cfg.Weather.RefreshMinutes) * time.Minute
	ttEvery := time.Duration(cfg.Timetable.RefreshMinutes) * time.Minute

	go runFetch(ctx, "天气", weatherEvery, func(ctx context.Context) (bool, error) {
		w, err := d.fetchWeather(ctx, now())
		if err != nil || w == nil {
			return false, err
		}
		d.setWeather(w)
		return true, nil
	}, nil, logger)

	go runFetch(ctx, "课表", ttEvery, func(ctx context.Context) (bool, error) {
		tt, err := d.fetchTimetable(ctx, now())
		if err != nil || tt == nil {
			return false, err
		}
		d.setTimetable(tt)
		return true, nil
	}, nil, logger)

	if d.fetchSchedule != nil {
		// 整学期课表按天抓：一学期里它基本不变，抓太勤只是白耗电与流量。
		// 「今日课程」那条线仍按 timetable.refresh_minutes 走。
		const scheduleEvery = 24 * time.Hour
		go runFetch(ctx, "整学期课表", scheduleEvery, func(ctx context.Context) (bool, error) {
			sched, err := d.fetchSchedule(ctx, now())
			if err != nil || sched == nil {
				return false, err
			}
			d.setSchedule(sched)
			return true, nil
		}, nil, logger)
	}

	if d.fetchCalendar != nil {
		const calEvery = 24 * time.Hour
		// fetchedYear 记住本次抓的是哪一年：抓取可能跨过午夜，等待时要据此
		// 判断是否需要马上补抓新年份。只在 runFetch 的同一 goroutine 里读写。
		var fetchedYear int
		go runFetch(ctx, "节假日", calEvery, func(ctx context.Context) (bool, error) {
			fetchedYear = now().Year()
			cal, err := d.fetchCalendar(ctx, fetchedYear)
			if err != nil || cal == nil {
				return false, err
			}
			if err := store.SaveCalendar(fetchedYear, cal); err != nil {
				logger.Printf("保存节假日缓存失败: %v", err)
			}
			firstLoad := !d.hasCalendarYear(fetchedYear)
			d.mergeCalendar(fetchedYear, cal)
			if firstLoad {
				// 跨年时这一行标记着新年份的第一份数据已到位。
				logger.Printf("节假日 %d 年已加载: %d 天", fetchedYear, len(cal))
			}
			return true, nil
		}, func(failures int) time.Duration {
			// 失败时保留原有的快速重试/退避；成功时才对齐年边界。
			if failures > 0 {
				return fetchWait(calEvery, failures)
			}
			return nextCalendarWait(fetchedYear, now(), calEvery)
		}, logger)
	}
}

// runFetch 是三条抓取线的共同骨架。
//
// 刻意不用泛型：T 为指针类型时，泛型函数里不能写 `v != nil`（Go 规定
// 类型参数不能与 nil 比较）。改为把「判断是否拿到数据并写入」封装进
// step，返回 done 表示是否成功拿到新数据。
//
// waitFn 可自定义下一次抓取前等多久；为 nil 时用 fetchWait(every, failures)。
func runFetch(
	ctx context.Context,
	name string,
	every time.Duration,
	step func(context.Context) (done bool, err error),
	waitFn func(failures int) time.Duration,
	logger *log.Logger,
) {
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		done, err := step(ctx)
		switch {
		case err != nil:
			failures++
			logger.Printf("%s 抓取失败（连续 %d 次）: %v", name, failures, err)
		case done && failures > 0:
			logger.Printf("%s 抓取恢复正常", name)
			failures = 0
		case done:
			failures = 0
		}

		wait := fetchWait(every, failures)
		if waitFn != nil {
			wait = waitFn(failures)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// fetchWait 决定下一次抓取前等多久。
//
// 分三段，每段都对应一个真实场景：
//
//   - 失败 1~2 次 → 30 秒后立刻重试。开机时 DNS 往往还没就绪，第一次抓取
//     必然失败；按正常周期等 10/30 分钟，等于开机后半小时才有数据
//     （真机日志里每次重启都能看到这两条失败）。
//   - 连续失败 3 次以上 → 退避到 5 分钟（但不超过原周期），对端真挂了时
//     不要一直每 30 秒打一次。
//   - 正常 → 原周期。
func fetchWait(every time.Duration, failures int) time.Duration {
	switch {
	case failures > 0 && failures < 3:
		return 30 * time.Second
	case failures >= 3 && every > 5*time.Minute:
		return 5 * time.Minute
	default:
		return every
	}
}

// calendarWait 决定下一次抓节假日数据前等多久。
//
// 取「常规周期」与「距下一个元旦零点」的较小者：跨年那一刻如果刚好落在
// 两次抓取之间，新年份的数据会缺席最长 24 小时，而设备 7×24 常驻，
// 元旦当天正是日历最该准的时候。
func calendarWait(now time.Time, every time.Duration) time.Duration {
	nextYear := time.Date(now.Year()+1, 1, 1, 0, 0, 5, 0, now.Location())
	if until := nextYear.Sub(now); until > 0 && until < every {
		return until
	}
	return every
}

// nextCalendarWait 决定抓完这一年之后等多久。
//
// 关键在 fetchedYear 与 now().Year() 的比较：抓取可能在午夜之前开始、
// 午夜之后结束（HTTP 超时上限 20 秒，窗口虽窄但真实）。那种情况下我们抓的是
// 旧年，新年份还没有数据 —— 必须很快补抓一次，不能按常规周期等 24 小时，
// 否则元旦当天依然空白，正是这次要消灭的缺口。
func nextCalendarWait(fetchedYear int, now time.Time, every time.Duration) time.Duration {
	if now.Year() != fetchedYear {
		return 5 * time.Second
	}
	return calendarWait(now, every)
}

// initctlPath 是 upstart 控制命令。抽成变量是为了测试能指向假脚本
// （设备上它在 /sbin/ 下，默认 PATH 里没有）。
var initctlPath = "/sbin/initctl"

// yieldMarkerFile 是「用户主动让出屏幕给原生界面」的标记文件名。
const yieldMarkerFile = "yield_to_gui"

// yieldMarkerPath 返回标记文件在 work_dir 下的完整路径。
func yieldMarkerPath(workDir string) string {
	return filepath.Join(workDir, yieldMarkerFile)
}

// shouldYieldOnStart 报告启动时是否应当直接退出。
//
// 只有「标记存在」且「GUI 确实在跑」两条同时成立才退出：
//   - 标记可能残留（用户后来用 KUAL 想再看时钟，start.sh 会删它）；
//   - GUI 可能没起来（stop_gui_on_start 的正常启动流程），这时该照常跑。
func shouldYieldOnStart(markerPath string, guiRunning func() bool) bool {
	if _, err := os.Stat(markerPath); err != nil {
		return false
	}
	return guiRunning != nil && guiRunning()
}

// upstartJobRunning 报告某个 upstart 作业是否处于 start/running 或 start/starting。
//
// 用 initctl 而不是看进程名：作业状态是 upstart 的权威判断，
// 而进程名会因为重命名、包装脚本之类的原因不可靠。
//
// start/starting 必须算「在跑」：GUI 刚启动的几百毫秒里就是这个状态。
// 只认 start/running 的话，点「返回 Kindle 界面」后程序检测不到 GUI，
// 自愈逻辑会把表盘重新画回屏幕，正好盖住正在显示的 GUI —— 用户看到
// 「返回了但又变回台历」。
func upstartJobRunning(job string) bool {
	out, err := exec.Command(initctlPath, "status", job).Output()
	if err != nil {
		return false
	}
	s := string(out)
	return strings.Contains(s, "start/running") || strings.Contains(s, "start/starting")
}
