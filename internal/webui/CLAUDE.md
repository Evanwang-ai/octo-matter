# internal/webui/
> L2 | 父级: /CLAUDE.md

嵌入式 SPA: 编译时打包 static/ 目录, 通过 /ui/ 路由提供服务。

## 成员清单

webui.go:           //go:embed static 指令, FS() 返回 fs.FS 供 router 挂载
static/index.html:  完整的单文件 SPA (Mailbox, My Matters split-pane/List/Board, 项目管理, Preference 面板, 创建表单, 计划图)

## UI 架构

- 单文件 HTML: 所有 JS/CSS 内联, 无构建工具
- 认证: 复用 octo-web localStorage 裸键 (token/uid/name), embed 模式走 syncMatterAuth()
- 导航: Mailbox(user-level) / My Matters(List/Board + 状态/项目/模式/时间/附件筛选) / 项目 / 自动化 / Preference
- Mailbox 详情: `body_html` 只进 `sandbox=""` iframe; 纯文本/摘要走 Markdown fallback; 可从当前 Space 转 Matter; Agent Mail inbound 信件显示回复入口但依赖服务端 sender gate
- Mailbox 左侧显示 Agent Mail bindings; 当前只登记地址, sync paused 等服务端授权 gate
- 创建表单: 邮件式 (领队→协作→主题→Brief→附件), 支持 status=backlog(草稿) 和 status=open(发送)
- 状态图标: Linear 风格 SVG 圆形 icon (statusIconSVG)
- 领队选择器: ownedBotsOnly (只显示自己的 bot + 自己)
- 协作选择器: humansOnly (只显示人类)

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
