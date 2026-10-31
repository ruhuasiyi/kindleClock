# KindleClock

把越狱的 Kindle Paperwhite 3 当作桌面时钟使用：显示时间、日期、农历和节气、天气、
月历，以及当天的课程表。页面为横版布局，支持触摸操作。

抓取和渲染都在设备上完成，不需要额外的服务器或常驻主机。

## 功能

- 时间，每分钟更新，只刷新时钟所在的屏幕区域
- 公历日期、星期、农历日期、二十四节气
- 法定节假日与调休标记
- 天气：当前天气、温度区间、空气质量
- 月历：可按月翻页，可点选日期查看那天的课程
- 课程表：当天剩余课程；翻到其他日期时显示那天的全部课程
- 电量百分比
- 设置页：返回原生界面、全屏刷新、背光调节、手动刷新课表、设备信息
- 屏幕自愈：内容被其他界面覆盖时，一分钟内自动重绘

## 硬件与系统要求

- Kindle Paperwhite 3（在固件 5.15.1.1 上验证），已越狱并能通过 SSH 以 root 登录
- 设备自带 `/usr/bin/fbink`（FBInk 1.25.0）和 `/usr/java/lib/fonts/STHeitiMedium.ttf`
- 建议常接 USB 供电

设备上没有 `eips`，系统 `wget` 的 TLS 版本过旧，无法访问现代站点，因此网络请求由
Go 标准库完成。整个项目只需要一个静态 ARM 二进制，不向 rootfs 安装任何依赖。

## 安装

### 用发布包（推荐）

从 [Releases](../../releases) 下载 `kindleclock-vX.Y.Z.zip`。

USB 连接设备后，`/mnt/us` 会作为磁盘挂载出来，**那个磁盘的根目录就是 `/mnt/us`**。
把 zip 解压到磁盘根目录即可，包里的结构已经对好：

```
kindleClock/               → 程序、配置、开机自启脚本
extensions/kindleClock/    → KUAL 扩展
```

解压后设备上应当是 `/mnt/us/kindleClock/` 和 `/mnt/us/extensions/kindleClock/` 两个目录。

然后：

1. 把 `/mnt/us/kindleClock/config.example.toml` 复制一份改名为 `config.toml`，
   填入天气城市、时区偏移等配置（见下一节）。这个文件不能提交，也不要放在公开位置。
2. 推出 USB，在 KUAL 的 KindleClock 菜单里点「启动」。

FAT32 上拷贝文件后可能没有执行权限。如果启动时报权限错误，用 SSH 登录设备执行
`chmod +x /mnt/us/kindleClock/kindleclock`。

**更新版本时**要先停掉正在运行的时钟再覆盖二进制 —— 文件被占用时 FAT32 上无法覆盖。
在 KUAL 里点「停止」，或者 `kill` 掉进程之后再解压。

### 自己构建

需要 Go 1.21 以上。

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -trimpath \
  -ldflags "-s -w -X main.buildVersion=$(git rev-parse --short HEAD)" \
  -o build/kindleclock ./src/kindleclock
```

产物是静态链接的 ARMv7 程序，约 7.4 MB。可以用 `readelf -h` 确认 `Machine: ARM`，
用 `file` 确认 `statically linked`。版本号会显示在设置页的设备信息里，不注入时显示 `dev`。

把二进制放到 `/mnt/us/kindleClock/kindleclock`，`kindleClock/` 目录放到
`/mnt/us/extensions/` 下，配置按上一节处理。

## 配置

```sh
cp config.example.toml config.toml
```

编辑 `config.toml`。该文件已在 `.gitignore` 中，不会被提交。

主要配置项：

| 段 | 项 | 说明 |
|---|---|---|
| `[time]` | `utc_offset_minutes` | 设备时区名不是 IANA 格式，Go 无法加载，这里直接给 UTC 偏移 |
| `[weather]` | `city` | 天气查询的城市名 |
| `[weather]` | `refresh_minutes` | 天气刷新间隔 |
| `[timetable]` | `enabled` | 是否启用课程表 |
| `[display]` | `rotate` | `cw` 或 `ccw`，取决于设备横放时的朝向 |
| `[display]` | `full_refresh_every` | 每多少帧做一次全屏刷新以清除残影 |
| `[display]` | `stop_gui_on_start` | 是否停掉原生界面独占屏幕 |
| `[periods]` | `times` | 每节课的起止时间，用于把课程表里的节次换算成钟点 |

## 使用

### KUAL 菜单

把仓库里的 `kindleClock/` 目录拷贝到设备的 `/mnt/us/extensions/`，
KUAL 中会出现名为 KindleClock 的二级菜单：

- 启动 / 停止
- 立即全屏刷新（清除 E-Ink 残影）
- 显示状态
- 切换独占屏幕模式

独占屏幕模式会停掉原生界面，此时 KUAL 本身也不可用，需要用 SSH 恢复：
`sh /mnt/us/extensions/kindleClock/bin/stop.sh`。

## 数据源

| 数据 | 来源 | 刷新间隔 |
|---|---|---|
| 天气 | uapis.cn 的天气接口 | 30 分钟 |
| 农历、节气、节假日 | uapis.cn 的节假日万年历接口，按年抓取后本地缓存 | 24 小时 |
| 课程表 | 学校教务系统（私有实现，本仓库不包含） | 24 小时，可在设置页手动刷新 |

**关于课程表**：本仓库中 `src/internal/source/timetable_stub.go` 是一个桩实现，所有抓取调用
都返回「课表功能未启用」。真实的接口实现不随仓库分发。需要课程表功能时，把私有实现放进
`src/internal/source/`，并在构建时加上 `-tags privatesource`。

其余数据在课程表未启用时照常工作。如何接入自己的数据源见
[`docs/timetable-integration.md`](docs/timetable-integration.md)。

## 实现要点

- 画面先按 1448×1072 的横版绘制，再整体旋转成 1072×1448 交给 fbink。
- 屏幕划分为 6 个矩形区域，每帧只对内容变化的区域做哈希比对并局部刷新，时钟区域实测
  刷新耗时约 13 ms。
- 每 240 帧做一次全屏刷新，清除累积的残影。
- 程序每分钟读取一次帧缓冲，与自己的渲染结果比对；若被其他界面覆盖则整屏重绘。
- 字体文件用 mmap 加载，避免把 21 MB 字体读进堆内存。

## 目录结构

```
src/kindleclock/     主程序
src/internal/        各功能包（配置、数据源、渲染、显示、触摸、交互）
kindleClock/         KUAL 扩展（解压到 /mnt/us/extensions/ 下）
.github/             GitHub Actions：打 tag 时构建并发布 zip
docs/                文档
```


## 已知限制

- 只在 Paperwhite 3 上验证过。其他型号的屏幕参数、触摸设备节点、字体路径可能不同。
- E-Ink 局部刷新会累积残影，需要依赖周期性全屏刷新清除。
- 拖动类操作（例如调节背光滑块）只在抬手时更新画面，不做实时跟手。

## 许可

待定。
