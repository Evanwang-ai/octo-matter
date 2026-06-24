# internal/webui/
> L2 | 父级: /CLAUDE.md

嵌入式 SPA: 编译时打包 static/ 目录, 通过 /ui/ 路由提供服务。

## 成员清单

webui.go:           //go:embed static 指令, FS() 返回 fs.FS 供 router 挂载
static/index.html:  完整的单文件 SPA (收件箱 split-pane, 项目管理, Preference 面板, 创建表单, 计划图)

## UI 架构

- 单文件 HTML: 所有 JS/CSS 内联, 无构建工具
- 认证: 复用 octo-web localStorage 裸键 (token/uid/name), embed 模式走 syncMatterAuth()
- 导航: 收件箱(唯一 matter 入口) / 项目 / 自动化 / Preference
- 创建表单: 邮件式 (领队→协作→主题→Brief→附件), 支持 status=backlog(草稿) 和 status=open(发送)
- 状态图标: Linear 风格 SVG 圆形 icon (statusIconSVG)
- 领队选择器: ownedBotsOnly (只显示自己的 bot + 自己)
- 协作选择器: humansOnly (只显示人类)

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
