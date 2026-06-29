# internal/webui/
> L2 | 父级: /CLAUDE.md

嵌入式 SPA: 编译时打包 static/ 目录, 通过 /ui/ 路由提供服务。

## 成员清单

webui.go:           //go:embed static 指令, FS() 返回 fs.FS 供 router 挂载
static/index.html:  单文件 SPA (~10400 行), 全部 JS/CSS 内联, 无构建工具

## UI 架构

- 导航: 全部回路(My Loops, List/Board) / 收件箱(split-pane) / 项目 / 自动化 / Preference
- 认证: 复用 octo-web localStorage 裸键 (token/uid/name), embed 模式走 syncMatterAuth()
- 路由: hash-based (#/matters, #/matters/board, #/mailbox, #/matter/:id, #/project/:id, #/cards, #/automation, #/timeline)
- 创建表单: 邮件式 (领队→协作→主题→Brief→附件), status=backlog(草稿) / status=open(发车)
- 状态图标: Linear 风格 SVG 圆形 icon (statusIconSVG)
- 领队选择器: ownedBotsOnly (只显示自己的 bot + 自己)
- 协作选择器: humansOnly (只显示人类)

## CSS token 体系

:root 变量遵循 Octo 设计稿, 关键层次:
- --text (100%) → --text-1 (80%) → --text-2 (66%) → --text-3 (50%) → --text-4 (38%) → --text-5 (28%)
- --hover = --surface-hover = rgba(28,28,35,.055)
- --fill-1 (2.5%) → --fill-2 (4.5%) → --fill-3 (6%)

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
