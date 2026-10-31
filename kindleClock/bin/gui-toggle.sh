#!/bin/sh
# gui-toggle.sh — KUAL「切换独占屏幕模式」：改写 config.toml 里的
# display.stop_gui_on_start，然后重启 KindleClock 让它生效。
#
# 两种模式（spec §2 / spikes S4）：
#   false（默认）：原生界面继续跑，左上角会画系统状态栏（时钟、电量、Wi-Fi）。
#                  触摸正常，随时能回书库。
#   true （独占）  ：/sbin/stop lab126_gui，画面纯净、无状态栏；
#                  代价是 KUAL 和触摸随 lab126_gui 一起消失 —— 切过去之后
#                  **点不到本菜单**，只能用 SSH 跑 stop.sh（或重启设备）恢复。
#
# 与开机自启互斥：upstart job 用「start on started lab126_gui /
# stop on stopping lab126_gui」，开独占会让 job 刚拉起时钟就被自己的
# stop GUI 停掉；job 的 install.sh 只在安装时检查，这里必须再查一次。
#
# 改写必须限定在 [display] 段内：TOML 的段名在方括号里，同名键出现在
# 别的段时不能动。用 sed 的 /a/,/b/ 地址范围在 busybox 上不可靠 ——
# 结束地址会在起始行自匹配后立刻收尾（`/^\[display\]/,/^\[/` 就是这种写法，
# 范围只有一行）。所以用 display_value 判断键在不在，再按「在/不在」分两条路走。

D=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
[ -n "$D" ] && [ -f "$D/common.sh" ] || D=/mnt/us/extensions/kindleclock/bin
. "$D/common.sh"

mkdir -p "$WORK"

if [ ! -f "$CFG" ]; then
    fb_say "找不到 config.toml"
    exit 1
fi

# ---- 1. 读当前值（只看 [display] 段；缺键按 false 处理，与 internal/config 的零值一致）----
CUR=$(display_stop_gui 2>/dev/null || true)
if [ "$CUR" = "true" ]; then
    NEW=false
    MSG="已关闭独占屏幕模式，已重启"
else
    NEW=true
    MSG="已开启独占屏幕模式
KUAL 将不可用；退出用 SSH 跑 stop.sh"
fi

# 独占模式与开机自启现在可以共存：job 的停止条件改成了 `stopping dbus`
# （关机路径），不再依赖 lab126_gui 的起停，所以程序自己停 GUI 不会连带
# 停掉自己的 job。这里不再拒绝。
# 装过自启时还会重启 job，让它重新读配置 —— 否则改完配置只有进程知道，
# job 下次 respawn 起来读到的又是新配置，两边不一致。

# ---- 2. 备份 ----
# FAT32 上 sed -i 是先写临时文件再 rename，失败时原文件仍在；这里额外留一份
# 是因为 /mnt/us 只读挂载或掉电时仍可能出现半截文件，而配置丢了就要重填。
cp -f "$CFG" "$CFG.bak" 2>/dev/null

# ---- 3. 改写 ----
if display_stop_gui >/dev/null 2>&1; then
    # 键在 [display] 段里：只替换段内那一行
    awk -v v="$NEW" '
        /^[[:space:]]*\[/ { sec = $0 }
        sec ~ /^[[:space:]]*\[display\]/ && /^[[:space:]]*stop_gui_on_start[[:space:]]*=/ {
            print "stop_gui_on_start = " v
            next
        }
        { print }
    ' "$CFG" > "$CFG.new" && cat "$CFG.new" > "$CFG" && rm -f "$CFG.new"
else
    # 键不在 [display] 段里（可能整个文件都没有，也可能只在别的段）：
    # 插在 [display] 标题行后面，保持这一项和它的同伴在一起
    if grep -qE '^[[:space:]]*\[display\]' "$CFG" 2>/dev/null; then
        sed -i "/^[[:space:]]*\[display\]/a stop_gui_on_start = $NEW" "$CFG"
    else
        # 连 [display] 段都没有 —— 配置本身是坏的（主程序也起不来），
        # 补一个缺省段不如直接报错，别把坏配置改得更难诊断
        fb_say "config.toml 缺少 [display] 段"
        exit 1
    fi
fi

# ---- 4. 回读校验 ----
# 不信任写命令的返回值：/mnt/us 掉线时 cat 成功、内容没落盘是可能的。
# 回读同样只看 [display] 段，值不对就不重启 —— 半改的配置比不改更糟。
if [ "$(display_stop_gui 2>/dev/null)" != "$NEW" ]; then
    fb_say "写入失败，配置未改"
    exit 1
fi

# ---- 5. 重启使配置生效 ----
# 先停进程但保留原生界面（--keep-gui），再按需决定 GUI 的最终状态，
# 最后才启动 —— 顺序不能反：GUI 若在时钟之后启动，首页会盖住时钟，
# 直到下一个整分局部刷新才被盖回来。
#
# 装过开机自启时走另一条路：用 initctl 重启 job。用 stop.sh/start.sh 的话
# 会变成「job 停在 stopped、屏幕上却有一个不受管的进程」，之后崩溃就再也
# 没人拉起，而且下次重启读到的配置和现在不一致。
if upstart_job_installed; then
    if [ "$NEW" = "false" ] && ! gui_running; then
        /sbin/start lab126_gui >/dev/null 2>&1
        i=0
        while [ "$i" -lt 5 ]; do
            gui_running && break
            sleep 1
            i=$((i + 1))
        done
    fi
    # 清掉「用户要求返回原生界面」的标记。
    #
    # 不删的话：用户点过「返回 Kindle 界面」后标记会一直留着，job 的 script 块
    # 看到它就 exit 0，时钟根本起不来 —— 而这里会误报成「重启 job 失败」。
    # 切换独占模式本身就是「我要看时钟」的明确意图。
    rm -f "$WORK/yield_to_gui"

    /sbin/stop kindleclock >/dev/null 2>&1
    /sbin/start kindleclock >/dev/null 2>&1
    sleep 3
    if kc_pid >/dev/null; then
        fb_say_restore "$MSG" 3
        exit 0
    fi
    fb_say "重启 job 失败，看 /mnt/us/kindleclock/log/app.log"
    exit 1
fi

sh "$D/stop.sh" --keep-gui >/dev/null 2>&1

if [ "$NEW" = "false" ]; then
    # 刚从独占切回来：原生界面是停着的，得先把它拉起来
    if ! gui_running; then
        /sbin/start lab126_gui >/dev/null 2>&1
        # 等框架真正起来（最多 5 秒）再启动时钟
        i=0
        while [ "$i" -lt 5 ]; do
            gui_running && break
            sleep 1
            i=$((i + 1))
        done
        sleep 2
    fi
fi
# NEW=true 时不用在这里停 GUI：start.sh 自己会读配置并 /sbin/stop lab126_gui

sh "$D/start.sh" >/dev/null 2>&1

# start.sh 不画成功提示（它要避免在铺屏时留残字），所以成功的情况
# 在这里补一句；失败时 start.sh 已经把原因留在屏幕上了，别覆盖它。
if kc_pid >/dev/null; then
    fb_say_restore "$MSG" 3
fi
exit 0
