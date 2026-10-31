//go:build !privatesource

// 开源版本的课表抓取器桩。
//
// 仓库里保留与私有实现完全相同的导出 API，调用方（cmd/kindleclock）
// 不需要为两种构建方式分叉。真实实现位于 internal/source/timetable_impl.go，
// 它被 .gitignore 排除在开源仓库之外；需要真实功能时把该文件放回来，
// 并用 -tags privatesource 构建。

package source

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/rhsy/kindleclock/src/internal/state"
)

// errTimetableDisabled 是桩实现对所有抓取请求的固定返回。
var errTimetableDisabled = errors.New("课表功能未启用：本仓库是开源版本，不包含教务接口实现")

// TimetableFetcher 是私有实现的公开替身。
//
// 只保留 token 的最小内存存储：调用方在启动时恢复缓存 token、成功后
// 持久化 token 的代码路径照常运行，不必知道当前是哪种实现。
type TimetableFetcher struct {
	mu    sync.Mutex
	token string
}

// NewTimetableFetcher 构造桩。
//
// 参数与私有实现一致，但不会保存 baseURL / 学号 / 密码 —— 开源版本里
// 它们没有任何用途，也不该被留在内存里。
func NewTimetableFetcher(baseURL, studentID, password string, client *http.Client) *TimetableFetcher {
	return &TimetableFetcher{}
}

// SetToken 注入已缓存 token（从磁盘恢复）。
func (f *TimetableFetcher) SetToken(t string) {
	f.mu.Lock()
	f.token = t
	f.mu.Unlock()
}

// Token 返回当前 token，供持久化。
func (f *TimetableFetcher) Token() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token
}

// Fetch 在开源版本中永远返回未启用错误。
func (f *TimetableFetcher) Fetch(ctx context.Context, now time.Time) (*state.Timetable, error) {
	return nil, errTimetableDisabled
}

// FetchSchedule 在开源版本中永远返回未启用错误。
func (f *TimetableFetcher) FetchSchedule(ctx context.Context, now time.Time) (*state.Schedule, error) {
	return nil, errTimetableDisabled
}
