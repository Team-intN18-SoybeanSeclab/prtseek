#!/usr/bin/env bash
# 开发模式：后端(:8787) + 流量代理(:8788) 与 前端 next dev(:5173) 一起跑。
# 前端 /api 反代到后端；Ctrl-C 一并退出。
#
# 单二进制（前端内嵌）方式见 README「单二进制」一节，不走这个脚本。
set -euo pipefail
cd "$(dirname "$0")"

backend_pid=
frontend_pid=
cleanup() {
    trap - EXIT INT TERM
    for pid in "$backend_pid" "$frontend_pid"; do
        if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; fi
    done
    wait 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

command -v go >/dev/null || { echo 'Go is required.' >&2; exit 1; }
command -v node >/dev/null || { echo 'Node.js is required.' >&2; exit 1; }
[ -d web/node_modules ] || (cd web && npm ci)
mkdir -p .dev
go build -o .dev/prtseek-dev ./cmd/prtseek

# 后端（普通 go run，不内嵌前端）；并发 work agent 数在「系统设置」里配置。
.dev/prtseek-dev -data "$PWD/data" -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788 &
backend_pid=$!

# 前端热更新（Vite/Next dev server，/api 反代到 :8787）。
( cd web && exec node node_modules/next/dist/bin/next dev --hostname 127.0.0.1 --port 5173 ) &
frontend_pid=$!

echo "[dev] 后端 :8787 / 代理 :8788 / 前端 http://localhost:5173  (Ctrl-C 退出)"
while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$frontend_pid" 2>/dev/null; do
    sleep 1
done
if ! kill -0 "$backend_pid" 2>/dev/null; then wait "$backend_pid"; else wait "$frontend_pid"; fi
