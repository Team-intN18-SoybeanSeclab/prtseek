# 清理与验证记录

## 当前工作台

- 前端采用统一的战术控制台界面：石墨灰底色、信号黄主操作、信息青状态提示、直角信息面板和高密度等宽排版。
- CTF 模式使用 `ctf_triage`、`ctf_planner`、`ctf_worker` 与 `ctf_writeup` 四阶段协作；提示词优先收敛 flag 格式、校验链路、关键证据与可复现步骤。
- 外部联动、远程安装流程及对应系统工具已移除；历史数据库会在启动时清理已废弃的连接设置和工具目录项。

## 建议验证命令

```bash
go test ./...
cd web
npm test
npx tsc --noEmit
npm run audit:unused
npm run build:static
```

数据库与服务端测试使用独立临时 SQLite 文件。生产构建使用 `web/out/` 作为静态前端产物；启动后可通过健康检查、登录、CTF 模式切换、工具目录及移动端布局进行浏览器回归。
