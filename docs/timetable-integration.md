# 接入自己的课表数据源

本仓库不包含任何学校教务系统的接口实现。课表部分是一个桩（`src/internal/source/timetable_stub.go`），
所有抓取调用都返回「课表功能未启用」。

需要课程表功能时，你自己写一份实现替换掉这个桩。这份文档说明实现需要满足的约定。

## 为什么这样拆

各学校的教务系统接口、字段、鉴权方式都不一样，也没有公开标准。把某一份具体实现放进通用项目里， 既没有通用性，也会泄漏那套接口的细节。所以这里只定义
**程序需要什么数据**， 至于怎么从你的教务系统拿到，由你决定。

## 接口约定

新建 `src/internal/source/timetable_impl.go`，首行加构建标签：

```go
//go:build privatesource
package source
```
文件里需要提供与桩完全相同的导出 API：

```go
type TimetableFetcher struct { /* 你的字段 */ }
func NewTimetableFetcher(baseURL, studentID, password string, client *http.Client) *TimetableFetcher
func (f *TimetableFetcher) SetToken(t string)
func (f *TimetableFetcher) Token() string
func (f *TimetableFetcher) Fetch(ctx context.Context, now time.Time) (*state.Timetable, error)
func (f *TimetableFetcher) FetchSchedule(ctx context.Context, now time.Time) (*state.Schedule, error)
```
`client` 为 nil 时要自己造一个带超时的客户端，不要用 `http.DefaultClient` ——
抓取协程是同步等待这一趟返回的，连接挂住会让这条抓取线永久停摆且没有任何日志。

`now` 是**配置时区**下的当前时间，用它给抓到的数据打时间戳、拼课程的绝对时间， 不要用 `time.Now()`。

## 需要产出什么

### `Fetch`——今天还剩什么课

主界面课程区在「跟随今天」时显示这个。返回 `\*state.Timetable`：

```go
type Timetable struct {
    Week      int       // 当前教学周，界面用来显示「第 N 教学周」
    Semester  string    // 学期名
    Courses   []Course  // 今天尚未结束的课，按开始时间升序
    Message   string    // 没有课时的说明文字，如「今日暂无后续课程」
    FetchedAt time.Time // 抓取时刻，界面显示「数据 HH:MM」并在过期时标注
    Available bool      // false 时界面显示不可用提示
}
type Course struct {
    Name      string
    StartText string // "14:15"，界面直接显示
    EndText   string // "14:55"
    Start     time.Time // 当天绝对时间，用于排序与「下一节课」判定
    End       time.Time
    Section   string // 可选，「5-6节」
    Room      string
    Teacher   string
}
```
只放**今天尚未结束**的课。已经上完的不要放进来。

### `FetchSchedule`——整学期课表

用户翻到其他日期时，程序用它裁剪出那天的课。返回 `\*state.Schedule`：

```go
type Schedule struct {
    Week      int       // 抓取时刻是第几教学周，这是推算其他日期的锚点
    Semester  string
    MaxWeek   int       // 一学期有多少周
    Specs     []CourseSpec
    FetchedAt time.Time
}
type CourseSpec struct {
    Name      string
    Teacher   string
    Room      string
    DayOfWeek int   // 1=周一 … 7=周日
    StartSection int // 第几节开始，1 起
    EndSection   int // 第几节结束，含
    Weeks     []int // 这门课在第几周上
}
```
`Week` 必须是**抓取那一刻**的教学周，程序以它为锚点按「相隔几个周一」推算其他日期属于第几周 （见 ` `src/internal/state/schedule.go` 的 `WeekOf`）。不需要提供学期起始日。

`MaxWeek` 用来判断非教学周；为 0 时不启用该判断。

## 节次怎么变成钟点

`CourseSpec` 里存的是节次，不是时间。程序用配置里的 `\[periods].times` 换算：

```toml
[periods]
times = ["08:20-09:00", "09:05-09:45", ...]
```
第 N 节的时间取自数组的第 N 个元素。所以这个数组必须与你们学校的作息一致， 且**长度不少于课程表里出现的最大节次**
——超出的课程会被跳过（只跳过那一门，不影响其他课）。

如果你的教务系统直接给时间区间而不是节次，在 `FetchSchedule` 里自己换算成节次即可， 或者反过来在 `Fetch` 里用时间区间、在 `
FetchSchedule` 里按 `times` 反查节次。

## 构建

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -tags privatesource -trimpath \
  -ldflags "-s -w -X main.buildVersion=$(git rev-parse --short HEAD)" \
  -o build/kindleclock ./src/kindleclock
```

多个标签用逗号分隔。日常开发与测试：

```sh
go build -tags privatesource ./src/kindleclock
go test -tags privatesource ./...
```
不带标签时构建的是桩版本，`go build ./...` 与 `go test ./...` 应当照常通过—— 你的实现不应当让公开构建失败。

## 配置

```toml
[timetable]
enabled = true
base_url = "https://your-backend.example.com"
student_id = "你的学号"
password = "你的密码"
refresh_minutes = 10      # 今日课表的刷新间隔
stale_after_minutes = 60  # 超过多久算过期，界面标注「已过期」
```
`base_url` 必须是 `http://` 或 `https://` 开头，否则配置校验会拒绝。 整学期课表的刷新周期固定 24
小时，不受这里影响；也可以在设置页手动刷新。

`config.toml` 含明文密码，且位于 `/mnt/us`（FAT32，插上 USB 就能读）。它已在 `.gitignore` 里，
不要提交，也不要放在公开可获得的位置。

## 错误处理

抓取失败时**不要清空已有数据**。程序会在失败时保留上一次的结果，并把界面标注为「已过期」或 「课表暂不可用」。你的实现只需要返回错误，缓存与降级由上层负责。

日志里绝对不要打印密码或 token。仓库里已有两条测试（`ErrorHidesPassword`、`ErrorHidesToken`）
检查这一点，建议你为自己的实现补上同样的测试。

## 一个完整的骨架

```go
//go:build privatesource
package source
import (
    "context"
    "errors"
    "net/http"
    "time"
    "github.com/rhsy/kindleclock/src/internal/state"
)
type TimetableFetcher struct {
    baseURL   string
    studentID string
    password  string
    client    *http.Client
    token     string
}
func NewTimetableFetcher(baseURL, studentID, password string, client *http.Client) *TimetableFetcher {
    if client == nil {
        client = &http.Client{Timeout: 20 * time.Second}
    }
    return &TimetableFetcher{baseURL: baseURL, studentID: studentID, password: password, client: client}
}
func (f *TimetableFetcher) SetToken(t string) { f.token = t }
func (f *TimetableFetcher) Token() string     { return f.token }
func (f *TimetableFetcher) Fetch(ctx context.Context, now time.Time) (*state.Timetable, error) {
    // 登录 → 拉今天的课 → 过滤掉已结束的 → 填 state.Timetable
    return nil, errors.New("not implemented")
}
func (f *TimetableFetcher) FetchSchedule(ctx context.Context, now time.Time) (*state.Schedule, error) {
    // 登录 → 拉整学期课表 → 填 state.Schedule
    return nil, errors.New("not implemented")
}
```
