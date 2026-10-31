#!/bin/sh
# refresh.sh — KUAL「立即全屏刷新」：对整屏做一次 GC16，抹掉局部刷新攒下的残影。
#
# 为什么不用 `kindleclock -once`（任务草稿里的做法）：
#   主程序用 flock 独占 work/kindleclock.lock（见 cmd/kindleclock/main.go），
#   第二个实例连配置都读不完就退出。实测：
#     $ ./kindleclock -config config.toml -once
#     启动失败: 已有实例在运行（.../kindleclock.lock 被锁）: resource temporarily unavailable
#   所以「再渲一帧推上去」这条路在常驻模式下根本走不通。
#
# 实际做法：`fbink -s -W GC16 -w` —— 不碰 framebuffer，只让 EPDC 用 GC16
# 波形重刷整个面板。屏幕内容不动，鬼影被清掉。实测 0.48 s。
# 主进程下一分钟照常只刷脏区，两者不冲突。

D=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
[ -n "$D" ] && [ -f "$D/common.sh" ] || D=/mnt/us/extensions/kindleclock/bin
. "$D/common.sh"

mkdir -p "$WORK"

if ! kc_pid >/dev/null; then
    fb_say_restore "KindleClock 未在运行" 3
    exit 0
fi

# -w 等波形真正落屏再返回，否则紧接着画的提示会和这次刷新抢 EPDC 队列
if "$FBINK" -q -s -W GC16 -w >/dev/null 2>&1; then
    fb_say_restore "已全屏刷新" 2
    exit 0
fi

fb_say_restore "刷新失败" 3
exit 1
