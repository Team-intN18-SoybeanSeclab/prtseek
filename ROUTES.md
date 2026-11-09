# 页面与菜单索引

当前为精简工作台。登录后进入 `/chat`，首屏只显示消息输入框和模型 / effort 控件。

| 页面 | URL | 入口 |
| --- | --- | --- |
| 对话 | `/chat` | 默认首页；左上角品牌返回 |
| Agents 管理 | `/system/agents` | 侧边栏 |
| LLM 管理 | `/system/llm` | 侧边栏 |
| 设置 | `/settings` | 侧边栏 |
| 文件工作区 | `/function/workspace` | 侧边栏 |
| 技能库 | `/system/skills` | 侧边栏 |
| 登录 | `/login` | 未登录时自动进入 |
| 首次设置密码 | `/setup` | 首次初始化时进入 |

Agent 管理提供普通五角色与 CTF 三角色，支持自定义角色增删查改。设置页提供主题、快捷键与认证配置。

`/chat?c=<会话ID>` 可恢复已有会话；不再提供会话列表侧栏。当前会话创建后会将 ID 写入地址，支持刷新恢复。

旧的 `/dashboard`、其他 `/function/*` 和其他 `/system/*` 页面已从前端移除，不再作为工作台入口。底层 Agent 执行、SQLite 历史记录和必要 API 不随界面精简删除。
