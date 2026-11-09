#!/bin/sh
# prtseek 守护启动脚本（Linux / macOS / Docker ENTRYPOINT）
#
# 用法：
#   ./start.sh                       前台运行（Ctrl-C 停止）
#   nohup ./start.sh >prtseek.log 2>&1 &   后台常驻
#   ./start.sh -addr :9000           额外参数原样透传给 prtseek
#
# 它只做一件事：把 prtseek 跑起来，进程退出后按退出码决定要不要再拉起。
#
#   0      用户正常停止        → 退出循环
#   75     程序请求重启        → 立刻重跑（保留退出码协议）
#   其他   崩溃                → 退避后重跑（1→2→4…最多 60 秒）
#
# 本脚本只管理进程与信号，不下载或替换二进制。
# prtseek 个人构建禁用上游自更新；退出码 75 继续保留为重启协议。
set -u

cd "$(dirname "$0")" || exit 1

BIN=./prtseek
if [ ! -x "$BIN" ]; then
    prtseek_OUTPUT="$PWD/prtseek" bash ./build.sh || exit 1
fi

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# 转发停止信号给 prtseek 本体。
#
# Docker 下这是必需的：docker stop 只把 SIGTERM 发给 PID 1（也就是本脚本），
# 不会发给子进程。不转发的话 prtseek 收不到信号、做不了优雅关闭，10 秒后被 SIGKILL
# 硬杀，正在跑的任务直接断在半路。
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# 信号会打断 wait 并让它返回 >128。此时子进程其实还在做优雅关闭，
	# 必须再 wait 一次才能拿到它真正的退出码。
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[prtseek] 已停止"
		exit 0
	fi

	case "$code" in
		0)
			echo "[prtseek] 正常退出"
			exit 0
			;;
		"$RESTART_CODE")
			# 应用请求重启；此脚本不下载或替换程序。
			echo "[prtseek] 请求重启…"
			delay=1
			;;
		*)
			echo "[prtseek] 异常退出 (code=$code)，${delay}s 后重启" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
