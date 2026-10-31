#!/bin/sh
# status.sh — KUAL「显示状态」：把运行状态画到屏幕上，看几秒后还原。
#
# 为什么不画完就走：主程序只重绘内容有变化的区域（internal/display 的
# Differ），状态文字盖住的那几块可能几小时不变，也就几小时不会被重画。
# 所以先 fbgrab 存一张、画状态、等几秒让人看清、再把那张贴回去。
#
# 代价：这几秒里若跨了整分，时钟会被还原成旧值，最多持续到下个整分
# （下个整分时钟内容变了，differ 判脏，自然重绘）。可接受。

D=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
[ -n "$D" ] && [ -f "$D/common.sh" ] || D=/mnt/us/extensions/kindleclock/bin
. "$D/common.sh"

mkdir -p "$WORK"

# ---- 1. 进程 ----
if _pid=$(kc_pid); then
    PROC="运行中 (pid $_pid)"
else
    PROC="未运行"
fi

# ---- 2. 原生界面 ----
if gui_running; then
    GUI="运行中"
else
    GUI="已停止"
fi

# ---- 3. 休眠锁 ----
SAVER=$("$LIPC_GET" com.lab126.powerd preventScreenSaver 2>/dev/null)
case "$SAVER" in
0) SAVER_TXT="关（会休眠）" ;;
1) SAVER_TXT="开（不休眠）" ;;
*) SAVER_TXT="读不到" ;;
esac

# ---- 4. 磁盘 ----
DISK=$(df -h /mnt/us 2>/dev/null | tail -1 | awk '{print $4}')
[ -n "$DISK" ] || DISK="?"

# ---- 5. 最后一次刷屏时间 ----
# 全屏刷新落 full.png，局部刷新落 frame-<id>.png，取两者中较新的一个。
# 只 stat 不读内容。date -r 在 busybox 上可用（实测）。
FRAME_TXT="无"
NEWEST=0
for f in "$WORK/full.png" "$WORK/frame-clock.png"; do
    [ -f "$f" ] || continue
    t=$(date -r "$f" +%s 2>/dev/null)
    [ -n "$t" ] || continue
    if [ "$t" -ge "$NEWEST" ]; then
        NEWEST=$t
        FRAME_TXT=$(date -r "$f" '+%m-%d %H:%M' 2>/dev/null)
    fi
done

# ---- 6. 最后一行日志 ----
# 按字节截断后必须再丢掉被切半的多字节字符，否则 fbink 遇到非法 UTF-8
# 会整条拒印（实测 rc=255，屏幕上一个字都不出）。
LAST=$(tail -1 "$LOG" 2>/dev/null | trunc_utf8 44)
[ -n "$LAST" ] || LAST="（无日志）"

LINES="KindleClock 状态
进程：$PROC
原生界面：$GUI
休眠锁：$SAVER_TXT
磁盘可用：$DISK
最后刷新：$FRAME_TXT
最后日志：$LAST"

# 8 秒：够看清七行，又不至于让 KUAL 转圈太久
fb_say_restore "$LINES" 8
exit 0
