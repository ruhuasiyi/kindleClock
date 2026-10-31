#!/bin/sh
# start.sh — KUAL「启动」：把 KindleClock 拉起来。
#
# 幂等：已经在跑就直接返回，不重复启动。
# 主程序用 flock 独占 work/kindleclock.lock，第二个实例会在读配置阶段
# 立刻退出并往日志里写一条「已有实例在运行」—— 所以必须先判活。

D=$(cd "$(dirname "$0")" 2>/dev/null && pwd)
[ -n "$D" ] && [ -f "$D/common.sh" ] || D=/mnt/us/extensions/kindleclock/bin
. "$D/common.sh"

mkdir -p "$WORK" "$DIR/log"

# 用户从 KUAL 明确点了「启动」= 现在想看时钟。清掉上次「返回 Kindle 界面」
# 留下的标记：KUAL 本身跑在 lab126_gui 里，GUI 正在运行，主程序启动时
# 若看到这个标记会立刻让出并退出 —— 表现就是「点了启动什么都没发生」。
rm -f "$WORK/yield_to_gui"

# ---- 日志轮转 ----
# 与 upstart job 相同的 1MB 上限：/mnt/us 只剩三百多 MB，不能让日志无限长。
# 滚动失败不拦启动。
if [ -f "$LOG" ] && [ "$(wc -c < "$LOG" 2>/dev/null || echo 0)" -gt 1048576 ]; then
    mv -f "$LOG" "$LOG.1" 2>/dev/null || true
fi

# ---- 1. 已在运行：不重复启动 ----
if _pid=$(kc_pid); then
    # 走到这里有两种可能：pid 文件正确，或 pid 文件过期而进程表里还有实例
    # （kc_pid 的兜底分支）。后者要把 pid 文件修正过来，否则下次 stop.sh
    # 还得再扫一遍进程表 —— 能修就修，别把陈旧状态留给下一个脚本。
    [ "$(cat "$PIDF" 2>/dev/null)" = "$_pid" ] || echo "$_pid" > "$PIDF"
    fb_say_restore "KindleClock 已在运行 (pid $_pid)" 3
    exit 0
fi
# 走到这里说明 pid 文件要么不存在，要么指向一个已经不在的 pid
[ -f "$PIDF" ] && rm -f "$PIDF"

# ---- 2. 前置检查：缺东西时给一句能看懂的话，别让进程在后台静默失败 ----
if [ ! -x "$BIN" ]; then
    fb_say "找不到 kindleclock 二进制，先跑 deploy.sh"
    exit 1
fi
if [ ! -f "$CFG" ]; then
    fb_say "找不到 config.toml"
    exit 1
fi

# ---- 3. 阻止休眠（跟随配置，与主程序一致） ----
# 主程序只在 display.prevent_screensaver = true 时才上锁；这里不无条件
# 设 1，否则用户显式写 false 会被 KUAL 启动覆盖。
if [ "$(display_prevent_saver 2>/dev/null)" = "true" ]; then
    "$LIPC_SET" com.lab126.powerd preventScreenSaver 1 2>/dev/null
fi

# ---- 4. 独占屏幕 ----
#
# 这里**不**停 lab126_gui，交给主程序自己停。
#
# 原因：KUAL 本身跑在 lab126_gui 里。从 KUAL 点「启动」时，本脚本是 GUI 进程树
# 的一部分 —— 在这里执行 /sbin/stop lab126_gui，等于把 GUI、KUAL 和本脚本
# 一起杀掉，时钟根本没机会启动，屏幕上只剩清屏后的白。
# 实测：从 KUAL 点启动 → 白屏、无进程；而 SSH 里手动跑同一个脚本却正常
# （SSH 会话不在 GUI 的进程树里）。
#
# 主程序启动时自己会做这件事（见 cmd/kindleclock 的 StopGUIOnStart），
# 而且是在它的首帧渲染之前，顺序是对的。

# ---- 5. 启动 ----
# cd 必须单独成句：写成 `cd X && nohup Y &` 时整条 and-list 会进后台子 shell，
# $! 拿到的是子 shell 的 pid，stop.sh 就杀不到真正的程序。
cd "$DIR" || {
    fb_say "无法进入 $DIR"
    exit 1
}
# setsid 把程序放进新会话：主程序起来后会去停 lab126_gui，而本脚本是 GUI
# 进程树的一部分 —— 不脱离会话就会被一起杀掉，时钟刚起就没了。
# stdin 接 /dev/null：KUAL 的 shell 退出后别把它的输入通道留给长期进程。
setsid nohup ./kindleclock -config "$CFG" >> "$LOG" 2>&1 < /dev/null &
echo $! > "$PIDF"

sleep 2

# ---- 6. 结果 ----
if kc_pid >/dev/null; then
    # 成功时不画提示：主程序此刻正在铺满全屏，画上去不是被盖掉就是留残字；
    # 屏幕上出现时钟本身就是最好的反馈。
    exit 0
fi

# 起不来时把最后一行日志一起显示
_last=$(tail -1 "$LOG" 2>/dev/null | trunc_utf8 44)
fb_say "启动失败
最后日志：$_last"
exit 1
