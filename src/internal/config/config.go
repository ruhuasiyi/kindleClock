// Package config 负责读取并校验 config.toml。
//
// 设计要点：Load 返回的 *Config 是不可变的快照，运行期不再读取文件。
// 校验失败必须给出能指到具体字段的错误信息 —— 设备上没有调试器，
// 日志是唯一的诊断渠道。
package config

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/image/font/sfnt"
)

type Config struct {
	Time      TimeConfig
	Weather   WeatherConfig
	Timetable TimetableConfig
	Display   DisplayConfig
	Periods   []Period
}

type TimeConfig struct {
	UTCOffsetMinutes int
}

type WeatherConfig struct {
	Enabled           bool
	City              string
	RefreshMinutes    int
	StaleAfterMinutes int
}

type TimetableConfig struct {
	Enabled           bool
	BaseURL           string
	StudentID         string
	Password          string
	RefreshMinutes    int
	StaleAfterMinutes int
}

type DisplayConfig struct {
	Rotate             string
	Fbink              string
	FontRegular        string
	FontBold           string
	WorkDir            string
	FullRefreshEvery   int
	PreventScreensaver bool
	StopGUIOnStart     bool
	// VerifyScreen 打开后，程序每分钟核对一次「屏幕上的是不是我们画的东西」，
	// 对不上就全屏重绘。电子墨水屏上这两者是两回事 —— 原生界面、KUAL 提示、
	// 框架重启都能把我们的画面盖掉，而脏区比对只知道自己上一帧画了什么。
	VerifyScreen bool
	// ShowBattery 控制是否显示电量。默认开。
	ShowBattery bool
}

// rawConfig 只负责 TOML 解码，字段带 toml tag。
// 解码后立刻转换成对外的 Config，避免把一串原始字符串散播到运行期。
type rawConfig struct {
	Time struct {
		UTCOffsetMinutes int `toml:"utc_offset_minutes"`
	} `toml:"time"`
	Weather struct {
		Enabled           bool   `toml:"enabled"`
		City              string `toml:"city"`
		RefreshMinutes    int    `toml:"refresh_minutes"`
		StaleAfterMinutes int    `toml:"stale_after_minutes"`
	} `toml:"weather"`
	Timetable struct {
		Enabled           bool   `toml:"enabled"`
		BaseURL           string `toml:"base_url"`
		StudentID         string `toml:"student_id"`
		Password          string `toml:"password"`
		RefreshMinutes    int    `toml:"refresh_minutes"`
		StaleAfterMinutes int    `toml:"stale_after_minutes"`
	} `toml:"timetable"`
	Display struct {
		Rotate             string `toml:"rotate"`
		Fbink              string `toml:"fbink"`
		FontRegular        string `toml:"font_regular"`
		FontBold           string `toml:"font_bold"`
		WorkDir            string `toml:"work_dir"`
		FullRefreshEvery   int    `toml:"full_refresh_every"`
		PreventScreensaver bool   `toml:"prevent_screensaver"`
		StopGUIOnStart     bool   `toml:"stop_gui_on_start"`
		VerifyScreen       *bool  `toml:"verify_screen"`
		ShowBattery        *bool  `toml:"show_battery"`
	} `toml:"display"`
	Periods struct {
		Times []string `toml:"times"`
	} `toml:"periods"`
}

// Load 读文件并校验。任何错误都带字段路径，例如 "display.rotate"。
func Load(path string) (*Config, error) {
	var raw rawConfig
	md, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}

	// 拼错的键会被 TOML 静默忽略，最终表现成「配置没生效」而不是报错。
	// 设备上没有调试器，这里必须炸出来。
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("配置里有未知字段: %s", strings.Join(keys, ", "))
	}

	// utc_offset_minutes 缺失时零值 0 落在合法区间内，会静默按 UTC 运行，
	// 时钟差 8 小时且毫无报错 —— 所以必须显式指定。
	if !hasKey(md, "time.utc_offset_minutes") {
		return nil, fmt.Errorf("time.utc_offset_minutes 必须显式指定（如 480 表示 +08:00）")
	}

	periods, err := parsePeriods(raw.Periods.Times)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Time: TimeConfig{
			UTCOffsetMinutes: raw.Time.UTCOffsetMinutes,
		},
		Weather: WeatherConfig{
			Enabled:           raw.Weather.Enabled,
			City:              raw.Weather.City,
			RefreshMinutes:    raw.Weather.RefreshMinutes,
			StaleAfterMinutes: raw.Weather.StaleAfterMinutes,
		},
		Timetable: TimetableConfig{
			Enabled:           raw.Timetable.Enabled,
			BaseURL:           raw.Timetable.BaseURL,
			StudentID:         raw.Timetable.StudentID,
			Password:          raw.Timetable.Password,
			RefreshMinutes:    raw.Timetable.RefreshMinutes,
			StaleAfterMinutes: raw.Timetable.StaleAfterMinutes,
		},
		Display: DisplayConfig{
			Rotate:             raw.Display.Rotate,
			Fbink:              raw.Display.Fbink,
			FontRegular:        raw.Display.FontRegular,
			FontBold:           raw.Display.FontBold,
			WorkDir:            raw.Display.WorkDir,
			FullRefreshEvery:   raw.Display.FullRefreshEvery,
			PreventScreensaver: raw.Display.PreventScreensaver,
			StopGUIOnStart:     raw.Display.StopGUIOnStart,
			// 默认开：关掉的话屏幕被盖之后只有时钟区会继续更新，
			// 其余部分永远停在别人的画面上，而且不会有任何报错。
			VerifyScreen: raw.Display.VerifyScreen == nil || *raw.Display.VerifyScreen,
			ShowBattery:  raw.Display.ShowBattery == nil || *raw.Display.ShowBattery,
		},
		Periods: periods,
	}

	// 默认值
	if cfg.Display.Fbink == "" {
		cfg.Display.Fbink = "fbink"
	}
	if cfg.Display.FullRefreshEvery <= 0 {
		cfg.Display.FullRefreshEvery = 240
	}
	if cfg.Weather.RefreshMinutes <= 0 {
		cfg.Weather.RefreshMinutes = 30
	}
	if cfg.Timetable.RefreshMinutes <= 0 {
		cfg.Timetable.RefreshMinutes = 10
	}
	if cfg.Weather.StaleAfterMinutes <= 0 {
		cfg.Weather.StaleAfterMinutes = 120
	}
	if cfg.Timetable.StaleAfterMinutes <= 0 {
		cfg.Timetable.StaleAfterMinutes = 60
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// hasKey 判断配置里是否出现过某个键（路径形如 "time.utc_offset_minutes"）。
//
// 不能用 md.IsDefined：它按字面比对，而 TOML 解码器不区分大小写，
// 于是 UTC_OFFSET_MINUTES 这类写法值能正常解出来、却被判成「没定义」。
// 这里用 EqualFold 比较键路径——实测 md.Keys() 返回的仍是原始大小写
// （"[time] UTC_OFFSET_MINUTES" 得到 "time.UTC_OFFSET_MINUTES"），
// 库并没有替我们小写化。
func hasKey(md toml.MetaData, path string) bool {
	for _, k := range md.Keys() {
		if strings.EqualFold(k.String(), path) {
			return true
		}
	}
	return false
}

// Validate 检查所有字段。返回第一个错误，错误信息以字段路径开头。
func (c *Config) Validate() error {
	if c.Time.UTCOffsetMinutes < -720 || c.Time.UTCOffsetMinutes > 840 {
		return fmt.Errorf("time.utc_offset_minutes 必须在 [-720, 840]，实际 %d", c.Time.UTCOffsetMinutes)
	}
	if c.Display.Rotate != "cw" && c.Display.Rotate != "ccw" {
		return fmt.Errorf("display.rotate 必须是 cw 或 ccw，实际 %q", c.Display.Rotate)
	}
	// spec §7.4：base_url「非空时」必须是合法 URL，与 enabled 无关。
	// 放在 Enabled 块之外，坏 URL 不会潜伏到某天开课表时才炸。
	if c.Timetable.BaseURL != "" {
		u, err := url.Parse(c.Timetable.BaseURL)
		if err != nil {
			return fmt.Errorf("timetable.base_url 不是合法 URL: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("timetable.base_url 必须以 http:// 或 https:// 开头，实际 %q", c.Timetable.BaseURL)
		}
		if u.Host == "" {
			return fmt.Errorf("timetable.base_url 缺少主机名，实际 %q", c.Timetable.BaseURL)
		}
	}
	if c.Timetable.Enabled {
		if c.Timetable.BaseURL == "" {
			return fmt.Errorf("timetable.base_url 不能为空（enabled=true 时必填）")
		}
		if c.Timetable.StudentID == "" {
			return fmt.Errorf("timetable.student_id 不能为空")
		}
		if c.Timetable.Password == "" {
			return fmt.Errorf("timetable.password 不能为空")
		}
	}
	if c.Display.WorkDir == "" {
		return fmt.Errorf("display.work_dir 不能为空")
	}
	if err := checkExecutable(c.Display.Fbink); err != nil {
		return fmt.Errorf("display.fbink: %w", err)
	}
	if err := checkFont(c.Display.FontRegular); err != nil {
		return fmt.Errorf("display.font_regular: %w", err)
	}
	if err := checkFont(c.Display.FontBold); err != nil {
		return fmt.Errorf("display.font_bold: %w", err)
	}
	return nil
}

// checkExecutable 接受绝对路径或裸命令名。
func checkExecutable(name string) error {
	if strings.ContainsRune(name, filepath.Separator) {
		st, err := os.Stat(name)
		if err != nil {
			return fmt.Errorf("不存在: %w", err)
		}
		// 目录天然带 x 位，必须先排除，否则 fbink="/某个目录" 会混过去。
		if !st.Mode().IsRegular() {
			return fmt.Errorf("不是普通文件: %s", name)
		}
		if st.Mode()&0o111 == 0 {
			return fmt.Errorf("不可执行: %s", name)
		}
		return nil
	}
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("在 PATH 中找不到 %q", name)
	}
	return nil
}

// checkFont 要求路径存在且能被 sfnt 解析。
// 只 stat 不够：指向目录或损坏的 TTF 要等到渲染时才炸，那时已经丢了字段名。
func checkFont(p string) error {
	if p == "" {
		return fmt.Errorf("路径不能为空")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("无法读取: %w", err)
	}
	if _, err := sfnt.Parse(data); err != nil {
		return fmt.Errorf("不是有效的 TTF/OTF: %w", err)
	}
	return nil
}
