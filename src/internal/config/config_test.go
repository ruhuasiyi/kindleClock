package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

const validTOML = `
[time]
utc_offset_minutes = 480

[weather]
enabled = true
city = "昆明"
refresh_minutes = 30
stale_after_minutes = 120

[timetable]
enabled = true
base_url = "https://timetable.example.com"
student_id = "202500000000"
password = "secret"
refresh_minutes = 10
stale_after_minutes = 60

[display]
rotate = "cw"
fbink = "true"
font_regular = "/tmp/font-test"
font_bold = "/tmp/font-test"
work_dir = "/tmp/kc-work"
full_refresh_every = 240
prevent_screensaver = true
stop_gui_on_start = false

[periods]
times = ["08:20-09:00", "09:05-09:45", "10:00-10:40"]
`

// writeTemp 把配置写进临时文件。
//
// 模板里的 /tmp/font-test 只是占位字体路径，真实机器上并不存在；
// 这里在临时目录里写一份真实的 Go Regular TTF（sfnt.Parse 能解析），
// 并把占位路径替换成它 —— 校验现在要求字体可解析，空文件不够。
// 需要测「字体缺失/损坏」的用例会先把 font_regular 换成别的路径再调用
// 本函数，因此不受影响。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	font := filepath.Join(dir, "font-test.ttf")
	if err := os.WriteFile(font, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}
	content = strings.ReplaceAll(content, "/tmp/font-test", font)
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	p := writeTemp(t, validTOML)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if cfg.Time.UTCOffsetMinutes != 480 {
		t.Errorf("UTCOffsetMinutes = %d, 期望 480", cfg.Time.UTCOffsetMinutes)
	}
	if cfg.Weather.City != "昆明" {
		t.Errorf("City = %q", cfg.Weather.City)
	}
	if cfg.Timetable.BaseURL != "https://timetable.example.com" {
		t.Errorf("BaseURL = %q", cfg.Timetable.BaseURL)
	}
	if cfg.Display.Rotate != "cw" {
		t.Errorf("Rotate = %q", cfg.Display.Rotate)
	}
	if len(cfg.Periods) != 3 {
		t.Fatalf("Periods 长度 = %d, 期望 3", len(cfg.Periods))
	}
	if cfg.Periods[0].StartMin != 8*60+20 || cfg.Periods[0].EndMin != 9*60 {
		t.Errorf("Period[0] = %+v", cfg.Periods[0])
	}
}

// show_battery 缺省时默认开；显式 false 时必须关掉。
// 默认值改成 false 会让这条测试变红。
func TestLoadShowBatteryDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, validTOML))
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if !cfg.Display.ShowBattery {
		t.Error("未写 show_battery 时应默认 true")
	}

	// validTOML 的 [display] 段在 [periods] 之前，插在它前面即属于 [display]。
	withFalse := strings.Replace(validTOML,
		"[periods]", "show_battery = false\n\n[periods]", 1)
	cfg, err = Load(writeTemp(t, withFalse))
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if cfg.Display.ShowBattery {
		t.Error("show_battery = false 时应为 false")
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML,
		"utc_offset_minutes = 480", "utc_offset_minutes = 480\nunknown_key = 1", 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "未知字段") ||
		!strings.Contains(err.Error(), "unknown_key") {
		t.Fatalf("期望未知字段错误并指出 unknown_key，得到 %v", err)
	}
}

// second_tick 曾经是个只解出来、从未被任何代码消费的配置项：改成 true
// 毫无效果，Undecoded 校验还会放行。现在删掉了，老配置必须报错而不是被
// 静默忽略 —— 宁可让用户看到「未知字段」，也不要留一个骗人的开关。
func TestLoadRejectsRemovedSecondTick(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML,
		"utc_offset_minutes = 480", "utc_offset_minutes = 480\nsecond_tick = true", 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "未知字段") ||
		!strings.Contains(err.Error(), "second_tick") {
		t.Fatalf("期望 second_tick 被当作未知字段拒绝，得到 %v", err)
	}
}

func TestLoadRejectsMissingUTCOffset(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML, "utc_offset_minutes = 480\n", "", 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "time.utc_offset_minutes") {
		t.Fatalf("期望 time.utc_offset_minutes 必填错误，得到 %v", err)
	}
}

// TOML 的键不区分大小写：UTC_OFFSET_MINUTES 照样解到 UTCOffsetMinutes。
// 「是否显式指定」的判定必须跟着解码器的语义走，否则合法配置会被误拒。
func TestLoadAcceptsUpperCaseUTCOffset(t *testing.T) {
	cfg, err := Load(writeTemp(t, strings.Replace(validTOML,
		"utc_offset_minutes = 480", "UTC_OFFSET_MINUTES = 480", 1)))
	if err != nil {
		t.Fatalf("大写键名应被接受，却报错: %v", err)
	}
	if cfg.Time.UTCOffsetMinutes != 480 {
		t.Errorf("UTCOffsetMinutes = %d, 期望 480", cfg.Time.UTCOffsetMinutes)
	}
}

func TestLoadRejectsBadRotate(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML, `rotate = "cw"`, `rotate = "right"`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "rotate") {
		t.Fatalf("期望 rotate 错误，得到 %v", err)
	}
}

func TestLoadRejectsBadPeriodFormat(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML,
		`["08:20-09:00", "09:05-09:45", "10:00-10:40"]`,
		`["8:20-9:00"]`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "HH:MM-HH:MM") {
		t.Fatalf("期望 HH:MM-HH:MM 格式错误，得到 %v", err)
	}
}

func TestLoadRejectsUnsortedPeriods(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML,
		`["08:20-09:00", "09:05-09:45", "10:00-10:40"]`,
		`["10:00-10:40", "08:20-09:00"]`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "升序") {
		t.Fatalf("期望升序错误，得到 %v", err)
	}
}

func TestLoadRejectsOffsetOutOfRange(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML,
		"utc_offset_minutes = 480", "utc_offset_minutes = 9999", 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "utc_offset_minutes") {
		t.Fatalf("期望 utc_offset_minutes 错误，得到 %v", err)
	}
}

func TestLoadRejectsBadBaseURL(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML,
		`base_url = "https://timetable.example.com"`, `base_url = "timetable.example.com"`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "timetable.base_url") {
		t.Fatalf("期望 timetable.base_url 错误，得到 %v", err)
	}
}

// spec §7.4：base_url「非空时」必须是合法 URL——与 enabled 无关。
// 否则坏 URL 会潜伏到某天打开课表时才炸。
func TestLoadRejectsBadBaseURLEvenWhenDisabled(t *testing.T) {
	content := strings.Replace(validTOML, "[timetable]\nenabled = true", "[timetable]\nenabled = false", 1)
	content = strings.Replace(content,
		`base_url = "https://timetable.example.com"`, `base_url = "timetable.example.com"`, 1)
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "timetable.base_url") {
		t.Fatalf("enabled=false 时坏 base_url 也必须报错，得到 %v", err)
	}
}

func TestLoadRejectsMissingFont(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML,
		`font_regular = "/tmp/font-test"`,
		`font_regular = "/nonexistent/definitely-missing.ttf"`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "font_regular") {
		t.Fatalf("期望 font_regular 错误，得到 %v", err)
	}
}

func TestLoadRejectsUnparsableFont(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.ttf")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	p := writeTemp(t, strings.Replace(validTOML,
		`font_regular = "/tmp/font-test"`,
		`font_regular = "`+garbage+`"`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "font_regular") {
		t.Fatalf("期望 font_regular 解析错误，得到 %v", err)
	}
}

func TestLoadRejectsMissingFbink(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML,
		`fbink = "true"`, `fbink = "definitely-not-a-binary-xyz"`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "fbink") {
		t.Fatalf("期望 fbink 错误，得到 %v", err)
	}
}

func TestLoadRejectsDirectoryAsExecutable(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, strings.Replace(validTOML,
		`fbink = "true"`, `fbink = "`+dir+`"`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "fbink") {
		t.Fatalf("期望 fbink 目录错误，得到 %v", err)
	}
}

func TestLoadRejectsEmptyWorkDir(t *testing.T) {
	p := writeTemp(t, strings.Replace(validTOML, `work_dir = "/tmp/kc-work"`, `work_dir = ""`, 1))
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "work_dir") {
		t.Fatalf("期望 work_dir 错误，得到 %v", err)
	}
}

func TestLoadAppliesRefreshDefaults(t *testing.T) {
	content := strings.Replace(validTOML, "refresh_minutes = 30\n", "", 1)
	content = strings.Replace(content, "refresh_minutes = 10\n", "", 1)
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if cfg.Weather.RefreshMinutes != 30 {
		t.Errorf("Weather.RefreshMinutes = %d, 期望 30", cfg.Weather.RefreshMinutes)
	}
	if cfg.Timetable.RefreshMinutes != 10 {
		t.Errorf("Timetable.RefreshMinutes = %d, 期望 10", cfg.Timetable.RefreshMinutes)
	}
}

func TestLoadAppliesStaleDefaults(t *testing.T) {
	content := strings.Replace(validTOML, "stale_after_minutes = 120\n", "", 1)
	content = strings.Replace(content, "stale_after_minutes = 60\n", "", 1)
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if cfg.Weather.StaleAfterMinutes != 120 {
		t.Errorf("Weather.StaleAfterMinutes = %d, 期望 120", cfg.Weather.StaleAfterMinutes)
	}
	if cfg.Timetable.StaleAfterMinutes != 60 {
		t.Errorf("Timetable.StaleAfterMinutes = %d, 期望 60", cfg.Timetable.StaleAfterMinutes)
	}
}
