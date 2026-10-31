#!/bin/sh
# stop.sh — KUAL「停止」：停掉 KindleClock 并把原生阅读界面还回去。
#
# 用法：
#   sh stop.sh                # 停进程 + 把屏幕还给原生界面
#   sh stop.sh --keep-gui     # 只停进程，不碰 lab126_gui（gui-toggle.sh 用）
#
# 设备事实（2026-10-30 实测）：
#   - 主程序收到 SIGTERM 后会跑完当前一帧再退出，所以要等、不能发完信号就走
#   - 对已在运行的 job 执行 /sbin/start 返回 rc=1 并打印
#     "Job is already running" —— 无害，但也不会重绘首页
#   - busybox 的 sleep 只保证整数秒

D=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
[ -n "$D" ] && [ -f "$D/common.sh" ] || D=/mnt/us/extensions/kindleclock/bin
. "$D/common.sh"

KEEP_GUI=0
[ "${1:-}" = "--keep-gui" ] && KEEP_GUI=1

# ---- 1. 停进程 ----
PID=$(kc_pid)
if [ -n "$PID" ]; then
    kill "$PID" 2>/dev/null

    # 最多等 5 秒优雅退出（main 的 runLoop 会先跑完当前一帧）
    i=0
    while [ "$i" -lt 5 ] && kill -0 "$PID" 2>/dev/null; do
        sleep 1
        i=$((i + 1))
    done
    # 只在确实还活着时补刀：等待结束时它可能已经退出（pid 还有被复用的窗口）
    if kill -0 "$PID" 2>/dev/null; then
        kill -9 "$PID" 2>/dev/null
    fi
fi
rm -f "$PIDF"

# ---- 2. 解除休眠锁 ----
"$LIPC_SET" com.lab126.powerd preventScreenSaver 0 2>/dev/null

# ---- 3. 把原生界面还回去 ----
# 只在配置是独占时才 /sbin/start：普通模式里 GUI 本来就该在跑，无条件 start
# 会把用户为省电自己停掉的 GUI 也拉起来。真正的「必须 start」场景是
# start.sh 为独占停掉 GUI 之后（此时配置里 stop_gui_on_start = true）。
if [ "$KEEP_GUI" = "0" ]; then
    if [ "$(display_stop_gui 2>/dev/null)" = "true" ]; then
        /sbin/start lab126_gui >/dev/null 2>&1
        sleep 1
    fi
    # GUI 本来就在跑时 /sbin/start 是 no-op，不会重绘；而时钟进程刚被杀，
    # 屏幕会一直停在最后一帧时钟（没人再刷它）。显式让框架回首页 ——
    # 用户点「停止」的语义就是「我要回书库」。GUI 没在跑时这条命令无效。
    gui_nudge
    sleep 2
fi

# ---- 4. 反馈 ----
# --keep-gui 是 gui-toggle 的内部调用：那边紧接着会显示自己的提示，
# 这里再闪一句「已停止」既误导又多一次整屏刷新。
if [ "$KEEP_GUI" = "0" ]; then
    # 等框架画完再截图 -> 画提示 -> 还原，最终留在屏幕上的是干净的首页，
    # 而不是压着一行「已停止」的首页。
    fb_say_restore "KindleClock 已停止" 3
fi
exit 0
