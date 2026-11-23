# prtseek

以deepseek系列模型驱动的CTF Agent。

本项目基于 [RuoJi6/ARTEX](https://github.com/RuoJi6/ARTEX) 二次开发，原始项目来自 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)。保留 Go 模块路径 `github.com/Autumn-27/artex` 和 AGPL-3.0 许可证。

## 项目改进

相较于ARTEX，prtseek删除了有关SRC挖掘等不包括在CTF范畴中的功能和代码，并将原始渗透Agent工作流改为CTF工作流。在CTF web环境中，使用传统的渗透agent通常会在尝试常用手法后停止测试，询问用户提供更多攻击面，缺乏对单个复杂环境的长时间自主研究能力

本项目在构建时专门针对CTF中单个目标四个小时以上的使用情况进行了优化，prtseek1.0版本发布当天，我们战队在第十届强网杯天基网络安全赛道CTF部分中获得第三名：

![image](./docs/images/qwb-2026-tianji-ranking-cropped.png)

本项目为deepseek v4.1 flash模型做了针对性优化，使用其他模型可能无法发挥最好的效果

## 快速启动

侧边栏保留 **Agents 管理、LLM 管理、工具管理、设置、文件工作区、技能库**，点击左上角品牌返回对话。工具管理支持查看和绑定系统工具，以及创建、测试、修改、启停、绑定和删除 shell、command、Python script、HTTP 自定义工具。
### Windows

源码首次运行需要 Go 1.26.10 或更高版本、Node.js 22 和 npm，加入 PATH。双击 `start-prtseek.cmd`，或执行：

```powershell
.\start-prtseek.cmd
```

首次自动安装前端依赖、构建内嵌前端的后端程序，然后打开 <http://127.0.0.1:18787/chat/>。服务在后台运行；重复打开会核验构建并复用同一实例。源码目录只使用 `.dev/prtseek.exe`，根目录的旧 exe 不再遮盖新构建。源码或程序指纹不一致时重新构建；已有旧服务运行时明确提示更新，不会静默打开旧版。

```powershell
# 修改源码后重新构建并启动
powershell -NoProfile -File .\start-prtseek.ps1 -Build
# git pull 后更新：先构建成功，再停止本安装目录的旧实例并启动新版
.\update-prtseek.cmd
# 不自动打开浏览器；可调整端口
powershell -NoProfile -File .\start-prtseek.ps1 -NoBrowser -Port 18787 -ProxyPort 18788
```

无 `go.mod` 的发行包直接使用根目录 `prtseek.exe`，无需 Go/Node.js。保留随包提供的 `.build.json` 和完整启动脚本。旧 `start.bat`、`start-personal.cmd` 入口继续转发到同一启动实现。更新会终止当前研究，请先让任务结束或在界面停止；编译失败则保留运行中的旧服务。需要完全停止服务时可在任务管理器中结束对应安装路径的 `prtseek.exe`。

### Linux / macOS

```bash
bash install.sh       # 从当前源码构建一次，产出 ./prtseek
sh start.sh          # 前台运行，默认 HTTP :8787、代理 127.0.0.1:8788
```

访问 <http://localhost:8787/chat/>。源码安装的 `start.sh` 会检查构建指纹，源码改变后先成功构建候选程序再替换。更新步骤：停止旧启动器/系统服务 → `git pull --ff-only` → `bash update.sh` → `sh start.sh`（或重启系统服务）。首次从旧版升级也必须停止旧进程；新版目录锁防止同一安装重复启动。运行发行包不需要构建工具；首次登录按页面提示设置密码，再在系统设置中配置模型。

### Docker

```bash
docker compose up --build -d
docker compose logs -f prtseek
docker compose down
```

Dockerfile 从当前源码构建前后端；Compose 只有 `prtseek` 服务，访问 <http://localhost:8787/chat/>。数据库与其他运行数据映射至宿主机 `data/`。`.env` 可选，变量示例见 [.env.example](.env.example)。

更新容器使用 `git pull --ff-only` 后执行 `docker compose up --build -d --force-recreate`，仅重启旧镜像不会更新程序。所有构建默认从 `VERSION` 读取版本；`/api/health` 的 `build_id` 可与 `/build-info.json` 对照。已打开页面检测到服务构建变化会提示刷新，并保留未发送输入直到用户主动刷新。页面与 RSC 文件禁止持久缓存，带内容哈希的静态资源仍可缓存。

首次从没有更新检测功能的旧页面升级时，请手动刷新一次以加载检测组件；后续版本变更才会自动显示提示。

升级保留原安装的 `data/`、`config.json` 和模型设置，不要用新空目录替换数据目录。Windows 更新入口只管理同一安装目录的进程，其他进程占用端口时会报错。

## 许可证

本项目沿用 [GNU AGPL-3.0](LICENSE)，保留上游版权和来源。二次开发构建不通过上游自动更新覆盖本项目程序。

## CTF 与 Agent 分派

输入框支持点击或拖放多个附件；文件进入会话隔离工作目录，Agent 收到可读取的绝对路径。`triage` 盘点材料，`command` 可通过 `delegate_agents` 从不同角度并行运行 2–6 个 `solver`；收敛结果可交给 `writeup` 生成 EXP 与 `WRITEUP.md`。子会话继承父会话实际模型配置，并在独立侧栏显示实时动态与查看链接。

Agent 管理仅展示四个默认 CTF Agent；每个角色可调整提示词、模型、轮次与工具可见性。新会话可从默认角色中选择入口，协作关系由默认角色提示词与分派结果定义。

`triage`、`command`、`solver` 新安装默认时间预算为 4 小时（14400 秒）；既有 Agent 的自定义设置会保留，升级后请在 Agents 管理核对时间与轮次。`writeup` 默认 30 分钟。子任务继承父任务截止时间，同时遵守自身预算，续研不会重置计时。

模型普通文字结尾只是阶段检查点。研究角色必须通过 `research_status` 提交本轮证据支持的完成结论或具体外部阻塞；未得到 flag 本身不构成阻塞。压缩后重新注入当前研究状态，保留证据、失败路线和下一步；中断后可继续会话，不自动重放工具副作用。连续三次结束检查没有新增工具证据会标记停滞，而非伪报成功。

整轮主任务和子任务默认仍共享 200 次逻辑模型请求（包括压缩），不是无限运行。长任务可在启动前设置 `prtseek_CTF_MODEL_CALL_LIMIT=2000`（范围 1–100000），会话概览显示实际限制；Windows PowerShell 使用 `$env:prtseek_CTF_MODEL_CALL_LIMIT='2000'` 后在同一终端运行更新入口，Docker 在 `.env` 配置。时间、轮次、调用预算、用户停止及真实外部阻塞中最先到达者生效。数小时续研已覆盖模拟时钟与协议回归，不代表完成真实数小时解题或保证拿到 flag。

## 更新日志

每次更新记录实际优化、修复和验证范围，完整历史见 [CHANGELOG.md](CHANGELOG.md)。

## 免责声明

1. 您的下载、安装、使用或修改本工具及相关代码，意味着您对本工具的信任。
2. 本工具在使用过程中可能对您或他人造成损失或伤害，若发生此类情况，我们不承担任何责任。
3. 如果您因使用本工具而从事任何非法行为，您将自行承担一切后果，并且我们不承担任何法律责任或连带责任。
4. 请在使用前，仔细阅读并充分理解所有条款，特别是关于责任免除或限制的条款，并自行决定是否接受。
5. 除非您已经完全理解并接受所有条款，否则您无法下载、安装、使用本工具。
6. 您的任何下载、安装、使用行为，均视为您已完全阅读并同意本协议条款。