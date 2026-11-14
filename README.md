# prtseek

一个极简的CTF Agent。

本项目基于 [RuoJi6/ARTEX](https://github.com/RuoJi6/ARTEX) 二次开发，原始项目来自 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)。保留 Go 模块路径 `github.com/Autumn-27/artex` 和 AGPL-3.0 许可证。

## 更新日志

每次更新记录实际优化、修复和验证范围，完整历史见 [CHANGELOG.md](CHANGELOG.md)。以下按日期倒序展示近期更新。

| 日期 | 更新 | 主要优化 |
| --- | --- | --- |
| 2026-11-14 | Agent 执行可靠性与证据管理 | 修复跨轮 Token 漏算和异常流误报完成；增加独立运行账本、HTTP 重定向与响应证据、正文去噪、停滞检查点、隔离实验及验证分级。 |
| 2026-11-11 | CTF 协作与交付流程 | 支持异步派发、等待和取消子任务，整轮共享模型调用预算；完善附件重试、目标报告、会话文件入口及模型连接测试。 |
| 2026-11-11 | 界面与 CI 修复 | 恢复开机动画，默认跟随系统主题，统一对话区与侧栏色调；修复跨平台构建检查及不稳定的依赖审计门禁。 |
| 2026-11-09 | CTF 工作台初始化 | 聚焦 CTF 四角色工作流，提供 Windows、Linux/macOS 和 Docker 启动方式，加入跨平台构建与冒烟测试。 |

## 项目改进

相较于ARTEX，prtseek删除了有关SRC挖掘等不包括在CTF范畴中的功能和代码，简化了前端，并将原始渗透Agent工作流改为CTF工作流。
使用此Agent，模型为DeepSeek V4.1 Flash，我们战队【踹哈公寓】在第十届强网杯天基网络安全赛道CTF部分中获得第三名：

![image](./docs/images/qwb-2026-tianji-ranking-cropped.png)

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

## 许可证

本项目沿用 [GNU AGPL-3.0](LICENSE)，保留上游版权和来源。二次开发构建不通过上游自动更新覆盖本项目程序。

## CTF 与 Agent 分派

输入框支持点击或拖放多个附件；文件进入会话隔离工作目录，Agent 收到可读取的绝对路径。`triage` 盘点材料，`command` 可通过 `delegate_agents` 从不同角度并行运行 2–6 个 `solver`；收敛结果可交给 `writeup` 生成 EXP 与 `WRITEUP.md`。子会话继承父会话实际模型配置，并在独立侧栏显示实时动态与查看链接。

Agent 管理仅展示四个默认 CTF Agent；每个角色可调整提示词、模型、轮次与工具可见性。新会话可从默认角色中选择入口，协作关系由默认角色提示词与分派结果定义。

