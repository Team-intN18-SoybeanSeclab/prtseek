# prtseek

本地 Agent 工作台，采用 Go 后端、Next.js 前端和内嵌 SQLite。登录后直接显示一个消息输入框，可选择模型与 reasoning effort；默认由 `mainagent` 接收输入。数据库随应用自动创建，无需安装数据库服务。

本项目基于 [RuoJi6/ARTEX](https://github.com/RuoJi6/ARTEX) 二次开发，原始项目来自 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)。保留 Go 模块路径 `github.com/Autumn-27/artex` 和 AGPL-3.0 许可证。前端采用独立的战术控制台设计语言：石墨灰基底、信号黄与信息青色状态提示，以及适合 CTF 研判的高密度信息层级。

## 快速启动

侧边栏保留 **Agents 管理、LLM 管理、工具管理、设置、文件工作区、技能库**，点击左上角品牌返回对话。工具管理支持查看和绑定系统工具，以及创建、测试、修改、启停、绑定和删除 shell、command、Python script、HTTP 自定义工具。普通模式提供 `goals`、`planner`、`mainagent`、`worker`、`reporter`，CTF 模式增加 `ctf_triage`、`ctf_planner`、`ctf_worker`、`ctf_writeup`，支持 Agent 增删查改。界面统一为明日方舟风格的战术控制台，提供深浅主题并适配手机布局；模型配置会读取服务商分页返回的完整模型列表，并支持按模型 ID 搜索。

概览、任务列表、资产、流量、计划任务等旧前端页面已移除。历史数据库记录与底层执行所需的数据结构仍保留；旧 Agent 不再出现在管理列表，也不能从管理 API 修改。已有对话可通过 `/chat?c=<会话ID>` 恢复，当前会话刷新后继续显示；点击“新对话”回到空白输入框。运行中锁定模型配置，停止或回复结束后可修改，下一轮生效。

### Windows

源码首次运行需要 Go 1.26.10 或更高版本、Node.js 22 和 npm，加入 PATH。双击 `start-prtseek.cmd`，或执行：

```powershell
.\start-prtseek.cmd
```

首次自动安装前端依赖、构建内嵌前端的后端程序，然后打开 <http://127.0.0.1:18787/chat/>。后续复用 `.dev/prtseek.exe`，不重复构建；终端中按 Ctrl-C 停止。

```powershell
# 修改源码后重新构建并启动
powershell -NoProfile -File .\start-prtseek.ps1 -Build
# 不自动打开浏览器；可调整端口
powershell -NoProfile -File .\start-prtseek.ps1 -NoBrowser -Port 18787 -ProxyPort 18788
```

若根目录已放置发行版 `prtseek.exe`，直接使用它，无需 Go/Node.js。旧 `start.bat`、`start-personal.cmd` 入口继续转发到同一启动实现。

### Linux / macOS

```bash
bash install.sh       # 从当前源码构建一次，产出 ./prtseek
sh start.sh          # 前台运行，默认 HTTP :8787、代理 127.0.0.1:8788
```

访问 <http://localhost:8787/chat/>。没有二进制时，`start.sh` 也会调用构建脚本。运行发行版只需要二进制和 `skills/`；首次登录按页面提示设置密码，再在系统设置中配置模型。

### Docker

```bash
docker compose up --build -d
docker compose logs -f prtseek
docker compose down
```

Dockerfile 从当前源码构建前后端；Compose 只有 `prtseek` 服务，访问 <http://localhost:8787/chat/>。数据库与其他运行数据映射至宿主机 `data/`。`.env` 可选，变量示例见 [.env.example](.env.example)。

## 数据与配置

默认主数据库为 `data/prtseek.db`，由 `-data` 指定的数据目录承载；直接调用数据库包时默认使用可执行文件旁的 `data/`。流量记录等独立存储仍在同一数据目录。SQLite 使用 WAL、外键约束和 10 秒忙等待；写事务使用 `BEGIN IMMEDIATE`，并发写入排队，读取可继续进行。任务入队顺序在数据库写操作内保证递增，不依赖系统时钟精度或时钟始终向前。

主数据库路径优先级：`prtseek_DB_PATH`（兼容 `ARTEX_DB_PATH`）→ `config.json` 的 `database.path` → 默认路径。显式相对路径按启动工作目录解析。

```json
{
  "database": { "path": "data/prtseek.db" },
  "skill_dir": "skills"
}
```

配置文件不是必需项。可复制 [config.example.json](config.example.json) 为 `config.json`，或通过 `prtseek_CONFIG` 指定文件。模型、工具和任务设置保存在 SQLite 中；API Key 可通过界面或对应环境变量配置。

**旧 PostgreSQL 数据不会自动导入 SQLite。** 旧 `PG_DSN` 或服务器连接字段会触发明确错误，避免悄悄打开空库。请先保留旧库和数据目录的备份，再指定新的 SQLite 文件。旧任务归档有单独的版本兼容逻辑，但不是完整 PostgreSQL 数据库迁移工具。不要把 `.dev/pgdata` 当作 SQLite 文件。

## 备份与更新

最简单的备份方式是停止应用后复制整个 `data/`，包括主数据库、独立流量数据库、JWT 密钥、证据和会话文件。运行中不能只复制 `prtseek.db`：已提交数据可能还在 `-wal` 文件中。需要在线数据库备份时使用 SQLite 的 backup 接口，并另外协调证据文件备份。

更新源码后：Windows 停止进程再使用 `-Build`；Linux/macOS 执行 `bash update.sh` 生成 `prtseek-next`，停止旧进程后替换二进制。构建和更新脚本不删除用户数据。

忘记密码时，可使用 `go run ./cmd/reset-password -db data/prtseek.db`，从标准输入输入新密码并换行。该工具要求目标数据库已存在。终端直接输入可能回显；Bash 可隐藏输入：

```bash
read -rs -p 'New password: ' password; echo
printf '%s\n' "$password" | bash reset-password.sh -db data/prtseek.db
unset password
```

## 开发与验证

```bash
bash dev.sh                 # Linux/macOS：后端 8787 + 前端 5173
go test ./db ./config       # SQLite 与配置测试，不需外部数据库
go test ./evidence ./server # 证据、归档、API 与任务调度回归
go test ./...               # 完整测试；部分工具测试需要 Bash/sed 等环境
cd web
npm ci
npm test                    # 前端逻辑回归
npm run audit:unused        # 未使用文件、依赖和导出检查
npm run build:static        # Windows/Linux 通用静态导出
```

数据库相关测试包自动建立独立临时 SQLite 文件，不使用生产库。完整测试还涉及本机文件锁、目录同步、脚本工具和 HTTP 服务；请根据输出区分平台限制与实现失败。

本轮精简后，数据库、Agent 与服务端测试通过，前端 45 个测试、TypeScript 与 Knip 检查通过。浏览器验证了单输入框首屏、五个菜单、五个 Agent、移动端导航及旧页面 404；使用模拟接口验证 model/effort 参数提交、停止、模型切换与刷新恢复。此前完成的 SQLite 和平台验证、以及本轮范围见[验证记录](docs/VALIDATION.md)。

通过不代表所有平台用例都执行：缺少符号链接权限时，对应用例会跳过；sed 静态推演的正向集成用例要求原生 `/bin/sed` 或 `/usr/bin/sed`，Windows 下跳过，Linux 环境执行。缺失执行文件、未知脚本变换和解析边界的测试仍会执行，生产代码继续将这些情况标为不完整。macOS 本机运行尚未验证；已添加 Windows/Linux/macOS 与 Docker 的 GitHub Actions 工作流，远端结果需实际触发后确认。

`python scripts/smoke.py <二进制路径>` 在临时目录验证健康检查、内嵌页面、SQLite 初始化与重启持久化；Unix 可加 `--launcher ./start.sh` 验证启动脚本和信号转发，此时二进制应位于启动脚本旁并命名为 `prtseek`。生产构建与开发服务共用 `.next` 构建缓存，请先停止 `npm run dev` 再执行静态构建；最终静态产物固定为 `web/out/`。

构建入口：Windows `build-prtseek.ps1` 生成 `.dev/prtseek-next.exe`；跨平台发行构建使用 `build.sh`，支持 `--target OS/ARCH` 与 `--release`。后端入口为 `cmd/prtseek`。

目录：`db/` 管理 SQLite 数据与查询，`server/` 提供 API 和任务调度，`agent/` 实现 Agent，`web/` 为前端，`skills/` 为技能，`evidence/` 与 `traffic/` 管理证据和流量。

## 许可证

本项目沿用 [GNU AGPL-3.0](LICENSE)，保留上游版权和来源。二次开发构建不通过上游自动更新覆盖本项目程序。

## CTF 与 Agent 分派

输入框可切换普通 / CTF / 自定义模式并上传多个附件；文件进入会话隔离工作目录，Agent 收到可读取的绝对路径。普通模式首条输入交给 mainagent 或 planner；自定义模式可从下拉框选择任意已启用的用户自定义 Agent；CTF 模式固定从 `ctf_triage` 开始，由它识别对话、附件、工作目录与容器材料的题型并交给 `ctf_planner`。Planner 先建立 goal，再通过 `delegate_agents` 从不同角度并行运行 2–6 个 `ctf_worker`；得到 flag 或验证链路后交给 `ctf_writeup` 生成 EXP 与 `WRITEUP.md`，最后汇总 final。各角色提示词、模型和 effort 可编辑，子会话继承父会话实际模型配置并显示进度与查看链接。

Agent 管理支持搜索、新增、编辑和删除，内置角色也可修改。删除会停用并隐藏角色、保留历史；以相同 key 新增可恢复原角色及提示词。删除入口角色后需恢复才能使用对应模式。运行中的角色须先停止会话。

