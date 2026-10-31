package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rhsy/kindleclock/src/internal/state"
)

// Store 把 last-good 数据与 token 落到磁盘。
//
// 目的：设备重启或断网时，界面仍有内容可显示。写入必须是原子的 ——
// 断电时不能留下半截文件，否则下次启动解析失败，冷启动就空白了。
type Store struct {
	dir string
}

// NewStore 构造。目录不必先存在，首次写入时会自动创建。
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name)
}

// writeAtomic 先写 .tmp、fsync、再 rename。
//
// 它提供的是「显著降低而非消除」写坏概率，原因如下：
//
//   - 目标设备是 FAT32（Kindle 的 /mnt/us）。vfat 的 rename 覆盖不是
//     ext4 那种「切换目录项指针」，而是复用目标文件原有的目录项、
//     原地改写其 start/size 字段（fs/fat/namei_vfat.c: vfat_rename）。
//     断电若落在这几个扇区的写入窗口内，可能留下「start 指向新簇但
//     size 还是旧值」的条目 —— 一个内容不一致但看起来合法的文件。
//   - 本机内核 3.0.35 的 fat_file_fsync 没有 blkdev_issue_flush
//     （v5.2 才加入，backport 到 4.19.80），所以 f.Sync() 只把数据推进块层，不冲刷设备写缓存。
//
// 但两个保证是真实的：
//   - fsync 保证数据先于目录项进入块层，把窗口压缩到最小的几个扇区
//   - rename 保证读者不会看到「长度已变而数据未到」的半截文件
//
// 真正的兜底不在这里，而在 Load* 解析失败 → 主循环回退到无缓存状态。
//
// 本函数假设全局单实例；多实例共用同一个 .tmp 会互相截断。
func (s *Store) writeAtomic(name string, v any) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s: %w", s.dir, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path(name) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("创建 %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("写 %s: %w", tmp, err)
	}
	// 数据先落盘
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("关闭 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path(name)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("重命名 %s: %w", tmp, err)
	}
	// 目录项已通过 rename 进入块层，这里的 Sync 是额外保险。
	// 注意目标设备 vfat 的目录从 3.0 起就有 .fsync，不会返回 EINVAL；
	// 这里仍忽略错误，因为此时数据已完整可见，报错只会把成功的写入判成失败。
	if d, err := os.Open(s.dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// readJSON 读取并解析 name。文件不存在或内容损坏都返回错误 ——
// 调用方据此判断「没有可用缓存」。
func (s *Store) readJSON(name string, v any) error {
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("解析 %s: %w", s.path(name), err)
	}
	return nil
}

// LoadWeather 读取缓存的天气。文件不存在或损坏时返回错误。
func (s *Store) LoadWeather() (*state.Weather, error) {
	var w state.Weather
	if err := s.readJSON("weather.json", &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// SaveWeather 原子写入天气缓存。
func (s *Store) SaveWeather(w *state.Weather) error {
	return s.writeAtomic("weather.json", w)
}

// LoadTimetable 读取缓存的课表。文件不存在或损坏时返回错误。
func (s *Store) LoadTimetable() (*state.Timetable, error) {
	var t state.Timetable
	if err := s.readJSON("timetable.json", &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// SaveTimetable 原子写入课表缓存。
func (s *Store) SaveTimetable(t *state.Timetable) error {
	return s.writeAtomic("timetable.json", t)
}

// LoadCalendar 读取某一年的节假日缓存。文件不存在或损坏时返回错误。
func (s *Store) LoadCalendar(year int) (map[string]state.DayInfo, error) {
	var c map[string]state.DayInfo
	if err := s.readJSON(fmt.Sprintf("calendar-%d.json", year), &c); err != nil {
		return nil, err
	}
	return c, nil
}

// SaveCalendar 写入某一年的节假日缓存。
//
// 按年分文件：跨年时旧年份的数据还能留着，翻回去看不用重新抓。
func (s *Store) SaveCalendar(year int, c map[string]state.DayInfo) error {
	return s.writeAtomic(fmt.Sprintf("calendar-%d.json", year), c)
}

// LoadSchedule 读取整学期课表缓存。
func (s *Store) LoadSchedule() (*state.Schedule, error) {
	var v state.Schedule
	if err := s.readJSON("schedule.json", &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// SaveSchedule 写入整学期课表缓存。
func (s *Store) SaveSchedule(v *state.Schedule) error {
	return s.writeAtomic("schedule.json", v)
}

// LoadToken 在文件不存在时返回 ("", nil) —— 首次运行是正常状态，不是错误。
// 返回的空串等同于「没有可用 token」，调用方无需再区分这两种情况。
func (s *Store) LoadToken() (string, error) {
	data, err := os.ReadFile(s.path("token.json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var v struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		// 错误信息只带文件名，不能带内容 —— 里面是 token。
		return "", fmt.Errorf("解析 token.json: %w", err)
	}
	return v.Token, nil
}

// SaveToken 原子写入 token。任何日志中都不得输出 token 本身。
func (s *Store) SaveToken(t string) error {
	return s.writeAtomic("token.json", map[string]string{"token": t})
}
