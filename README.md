# prtseek

一个极简的CTF Agent。

本项目基于 [RuoJi6/ARTEX](https://github.com/RuoJi6/ARTEX) 二次开发，原始项目来自 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)。保留 Go 模块路径 `github.com/Autumn-27/artex` 和 AGPL-3.0 许可证。

相较于ARTEX，prtseek

## 快速启动

侧边栏保留 **Agents 管理、LLM 管理、工具管理、设置、文件工作区、技能库**，点击左上角品牌返回对话。工具管理支持查看和绑定系统工具，以及创建、测试、修改、启停、绑定和删除 shell、command、Python script、HTTP 自定义工具。默认编组固定为 `triage`、`command`、`solver`、`writeup` 四个可编辑角色。

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

## 备份与更新

最简单的备份方式是停止应用后复制整个 `data/`，包括主数据库、独立流量数据库、JWT 密钥、证据和会话文件。运行中不能只复制 `prtseek.db`：已提交数据可能还在 `-wal` 文件中。需要在线数据库备份时使用 SQLite 的 backup 接口，并另外协调证据文件备份。

更新源码后：Windows 停止进程再使用 `-Build`；Linux/macOS 执行 `bash update.sh` 生成 `prtseek-next`，停止旧进程后替换二进制。构建和更新脚本不删除用户数据。

忘记密码时，可使用 `go run ./cmd/reset-password -db data/prtseek.db`，从标准输入输入新密码并换行。该工具要求目标数据库已存在。终端直接输入可能回显；Bash 可隐藏输入：

```bash
read -rs -p 'New password: ' password; echo
printf '%s\n' "$password" | bash reset-password.sh -db data/prtseek.db
unset password
```

## 许可证

本项目沿用 [GNU AGPL-3.0](LICENSE)，保留上游版权和来源。二次开发构建不通过上游自动更新覆盖本项目程序。

## CTF 与 Agent 分派

输入框支持点击或拖放多个附件；文件进入会话隔离工作目录，Agent 收到可读取的绝对路径。`triage` 盘点材料，`command` 可通过 `delegate_agents` 从不同角度并行运行 2–6 个 `solver`；收敛结果可交给 `writeup` 生成 EXP 与 `WRITEUP.md`。子会话继承父会话实际模型配置，并在独立侧栏显示实时动态与查看链接。

Agent 管理仅展示四个默认 CTF Agent；每个角色可调整提示词、模型、轮次与工具可见性。新会话可从默认角色中选择入口，协作关系由默认角色提示词与分派结果定义。

