#!/bin/sh
# common.sh — kual-prod 五个脚本的共用部分。
#
# 这个文件不单独执行，只被同目录的 start/stop/refresh/status/gui-toggle
# 「.」进来。放在一起是因为下面几条设备事实在五个脚本里都要用，
# 复制五份迟早会改漏一处：
#
#   - UPSTART：start/stop/initctl 在 /sbin/ 下，默认 PATH 里没有。
#   - 中文提示：fbink 内置字体（IBM，Unifont 未编译进这份 build）没有中文
#     字形，`fbink -m -M "中文"` 在屏幕上什么都不显示，只在 stderr 报
#     "Codepoint U+xxxx is not covered by this font"。凡给用户看的字
#     必须 -t regular=<STHeiti>。
#   - 进程识别：用 /proc/PID/comm（内核维护的 basename），见 kc_pids()。
#
#     PW3 / 5.15.1.1 实测：不带参数的 `ps` 输出是四列
#     `PID TTY TIME CMD`，CMD 列就是 comm —— 主程序以 ./kindleclock 启动，
#     但 ps 显示的是 `kindleclock`，所以 `ps | awk '$4 == "kindleclock"'`
#     在这台设备上**是可以用的**。
#
#     仍然改用 /proc/comm，理由不是「ps 一定错」，而是「ps 的列布局取决于
#     busybox 的编译配置和调用参数」：同一条命令换个 build 或加个选项，
#     $4 就从 CMD 变成 STAT 或 VSZ，脚本会在那种设备上静默失效。
#     /proc/comm 不受这些影响。不要再用 `ps | grep` —— 它匹配的是整行文本，
#     命令行列一旦含路径就会自匹配（是否发生取决于 ps 的实现，不该赌）。
#   - pid 文件会过期：进程被 kill -9 后文件还在，pid 还可能被系统复用。
#     文件里的 pid 必须同时出现在进程表里才算数。
#   - busybox 的 sleep 只保证整数秒。
#   - 截断日志行要按字节切再丢掉被切半的多字节字符，否则 fbink 遇到
#     非法 UTF-8 会整条拒印（rc=255）。

# 各脚本用 `D=$(cd "$(dirname "$0")" && pwd)` + 兜底固定路径来定位本文件：
# KUAL 按 action 里的相对路径执行（bin/start.sh），此时 $0 可能是相对的，
# 兜底成绝对路径就不会因为 cwd 变化而找不到 common.sh。

# 程序和配置在 /mnt/us/kindleClock/，本目录只是 KUAL 扩展，两者是分开的。
DIR=/mnt/us/kindleClock
BIN=$DIR/kindleclock
CFG=$DIR/config.toml
PIDF=$DIR/work/pid
LOG=$DIR/log/app.log
WORK=$DIR/work

FBINK=/usr/bin/fbink
[ -x "$FBINK" ] || FBINK=fbink
# fbgrab 与 lipc 工具同样优先绝对路径：KUAL 的 PATH 不一定可靠。
FBGRAB=/usr/bin/fbgrab
[ -x "$FBGRAB" ] || FBGRAB=fbgrab
LIPC_SET=/usr/bin/lipc-set-prop
[ -x "$LIPC_SET" ] || LIPC_SET=lipc-set-prop
LIPC_GET=/usr/bin/lipc-get-prop
[ -x "$LIPC_GET" ] || LIPC_GET=lipc-get-prop

FONT=/usr/java/lib/fonts/STHeitiMedium.ttf

# 提示用的临时截图。放 /var/tmp（tmpfs，不占 /mnt/us 那 369 MB）
SNAP=/var/tmp/kindleclock-toast.png

# 提示字号。33 是实测能在一行放下「KindleClock 已在运行 (pid 23410)」
# 又不至于在 1072 宽的屏上显得小的档位
SAY_PX=33

# 画一段提示（可含换行）。
#
# 优先走主程序的 -toast：它把文字画成图、按配置旋转成竖版再贴上去，
# 横放的设备上读得正。
#
# 为什么不能直接 `fbink -m -M`：fbink 按**竖版 framebuffer 坐标**画字，
# 而我们的内容整体旋转了 90°。实测（2026-10-30，真机 status.sh 截图）
# 直接用 fbink 印出来的七行状态全部竖着躺在表盘上。FBInk 1.25.0 的
# --help 里没有任何旋转文字的选项，只能自己画。
#
# 二进制或配置还不存在时（比如还没 deploy）退回 fbink：中文可能看不见，
# 但脚本本身不该因为少一个文件就崩掉。
fb_say() {
    if [ -x "$BIN" ] && [ -f "$CFG" ] &&
        "$BIN" -config "$CFG" -toast "$1" >/dev/null 2>&1; then
        return 0
    fi
    if [ -f "$FONT" ]; then
        "$FBINK" -q -m -M -t regular="$FONT",px="$SAY_PX" "$1" 2>/dev/null
    else
        "$FBINK" -q -m -M "$1" 2>/dev/null
    fi
}

# 提示 -> 停 N 秒 -> 把屏幕还原成提示前的样子。
#
# 为什么必须还原：主程序只重绘「内容有变化」的区域（internal/display
# 的 Differ），被提示盖住的日期、天气、月历可能几小时不变，也就几小时
# 不会被重画 —— 一行提示会一直挂在那里。fbgrab 存一张、画提示、
# 再用 -g 贴回去，实测前后 fbgrab 逐字节一致。
#
# 拿不到截图时：时钟在跑就放弃提示（宁可不显示，也不要把书库首页盖到
# 表盘上 —— 主程序只刷脏区，被盖住的天气/月历可能几小时不会重画）。
# 时钟已停时框架才是屏幕的主人，让它重绘首页把字抹掉。
fb_say_restore() {
    if "$FBGRAB" "$SNAP" >/dev/null 2>&1; then
        fb_say "$1"
        sleep "$2"
        "$FBINK" -q -g file="$SNAP",x=0,y=0 -W GC16 -w >/dev/null 2>&1
        rm -f "$SNAP"
        return 0
    fi
    if kc_pid >/dev/null 2>&1; then
        return 0
    fi
    fb_say "$1"
    sleep "$2"
    gui_running && gui_nudge
    return 0
}

# 让原生框架立刻重绘首页。实测：
#   - GUI 运行中：5 秒内把屏幕上我们画的东西全部抹掉
#   - GUI 停着：无效（没有进程处理这条命令），屏幕保持原样
gui_nudge() {
    "$LIPC_SET" com.lab126.appmgrd start app://com.lab126.booklet.home >/dev/null 2>&1
}

# 所有主程序实例的 pid（正常只有一个；flock 保证）。
#
# 用 /proc/PID/comm：它是内核维护的进程名（basename，截断到 15 字符），
# 与 argv[0] 的写法无关，也不会被 KUAL shell 自己的 cmdline 污染。
kc_pids() {
    for _d in /proc/[0-9]*; do
        [ -r "$_d/comm" ] || continue
        if [ "$(cat "$_d/comm" 2>/dev/null)" = "kindleclock" ]; then
            echo "${_d#/proc/}"
        fi
    done
}

# 打印正在运行的主程序 pid，返回 0；没在跑返回 1。
kc_pid() {
    _p=$(cat "$PIDF" 2>/dev/null)
    if [ -n "${_p:-}" ] && kill -0 "$_p" 2>/dev/null; then
        for _q in $(kc_pids); do
            if [ "$_q" = "$_p" ]; then
                echo "$_p"
                return 0
            fi
        done
    fi
    _p=$(kc_pids | head -1)
    [ -n "$_p" ] || return 1
    echo "$_p"
    return 0
}

# lab126_gui 的 upstart 状态行（停着/不存在时为空或 stop/waiting）。
gui_state() {
    /sbin/initctl status lab126_gui 2>/dev/null
}

# 开机自启 job 的路径。留环境变量覆盖口，方便本地回归测试（KUAL 不会设置它）。
UPSTART_CONF=${UPSTART_CONF:-/etc/upstart/kindleclock.conf}

# 是否装了开机自启 job —— 它与独占模式互斥（见 gui-toggle.sh）。
upstart_job_installed() {
    [ -f "$UPSTART_CONF" ]
}

# lab126_gui（原生阅读界面）是否在运行
gui_running() {
    case "$(gui_state)" in
    *start/running*) return 0 ;;
    *) return 1 ;;
    esac
}

# 等 lab126_gui 真正停稳（最多 N 秒，默认 5）。unknown job 视为已停。
#
# 为什么要等：GUI 关闭过程中的最后一次重绘若落在主程序的首帧全屏刷新
# 之后，会留在 6 个区域之外的 5 条空白带上 —— 局部刷新永远不碰那里，
# 最长要等 240 帧（约 4 小时）的周期全屏刷新才可能清掉。
wait_gui_stopped() {
    _n=${1:-5}
    _i=0
    while [ "$_i" -lt "$_n" ]; do
        case "$(gui_state)" in
        *stop/waiting*|"") return 0 ;;
        esac
        sleep 1
        _i=$((_i + 1))
    done
    return 1
}

# display_value KEY：打印 config.toml 中 [display] 段内 KEY 的原始值。
# 只认 [display] 段；键不存在时返回 1。
#
# 不能用全局 grep：别的段里出现同名键时会误判（start.sh 会因此误停 GUI）。
# 也不能用 sed 的 /a/,/b/ 地址范围 —— busybox 上结束地址会在起始行自匹配后
# 立刻收尾，范围只剩一行。
display_value() {
    awk -v key="$1" '
        /^[[:space:]]*\[/ { sec = $0 }
        sec ~ /^[[:space:]]*\[display\]/ && $0 ~ ("^[[:space:]]*" key "[[:space:]]*=") {
            line = $0
            found = 1
        }
        END {
            if (!found) exit 1
            sub(/^[^=]*=[[:space:]]*/, "", line)
            sub(/[[:space:]]*(#.*)?$/, "", line)
            print line
        }
    ' "$CFG" 2>/dev/null
}

display_stop_gui() { display_value stop_gui_on_start; }
display_prevent_saver() { display_value prevent_screensaver; }

# 把一段文本按字节截断成合法的 UTF-8。用法：printf '%s' "$s" | trunc_utf8 48
#
# fbink 遇到非法 UTF-8 会整条拒印（rc=255）—— 不是丢一行日志，而是整个
# 七行状态页 / 整条「启动失败」提示一个像素都不显示。所以两条路径都必须
# 产出合法 UTF-8，不能裸 head -c。
TRUNC_ICONV=0
if command -v iconv >/dev/null 2>&1; then
    TRUNC_ICONV=1
fi
trunc_utf8() {
    _n=${1:-48}
    if [ "$TRUNC_ICONV" = 1 ]; then
        # iconv -c 会丢掉被截断的尾部序列
        head -c "$_n" | iconv -c -f UTF-8 -t UTF-8 2>/dev/null
        return 0
    fi
    # 没有 iconv：按 UTF-8 前导字节判断末尾是否有不完整字符，只丢那一个。
    # LC_ALL=C 让 awk 按字节处理（length/substr 都以字节计）。
    # 调用方只传一行，所以这里按单条记录处理、printf 不补换行。
    head -c "$_n" | LC_ALL=C awk '
        BEGIN {
            # 用 sprintf("%c", n) 建字节 -> 数值的反查表。C locale 下
            # %c 输出单个原始字节；NUL 不会出现在日志行里，跳过。
            for (_i = 1; _i < 256; _i++)
                _ord[sprintf("%c", _i)] = _i
        }
        {
            _s = $0
            _i = length(_s)
            _k = 0
            # 从末尾数连续的续字节（0x80..0xBF）
            while (_i > 0) {
                _v = _ord[substr(_s, _i, 1)]
                if (_v >= 128 && _v <= 191) {
                    _k++
                    _i--
                    continue
                }
                break
            }
            if (_i == 0) {
                # 整段都是续字节（前导字节已被截掉）：全部丢弃
                _s = ""
            } else {
                _b = _ord[substr(_s, _i, 1)]
                if (_b < 128) _need = 1
                else if (_b >= 192 && _b <= 223) _need = 2
                else if (_b >= 224 && _b <= 239) _need = 3
                else if (_b >= 240 && _b <= 247) _need = 4
                else _need = 1
                # 前导字节 + 续字节不足该字符应有长度 -> 丢掉这一整个残字
                if (_k + 1 < _need)
                    _s = substr(_s, 1, _i - 1)
            }
            printf "%s", _s
        }
    '
}

