# internal/webui/
> L2 | 父级: /CLAUDE.md

嵌入式 SPA: 编译时打包 static/ 目录, 通过 /ui/ 路由提供服务。

## 成员清单

webui.go:           //go:embed static 指令, FS() 返回 fs.FS 供 router 挂载
static/index.html:  单文件 SPA (~10150 行), 全部 JS/CSS 内联, 无构建工具

## UI 架构

- 术语: "回路"(Loop) = 原"事项"(Matter), UI 文本全部改为"回路", 代码标识符保持 matter
- 终态: 只有 cancelled(已取消), 无 archived(已合并)。项目级 p.archived 独立保留
- 导航: 全部回路(My Loops, List/Board) / 收件箱(split-pane) / 项目 / 自动化 / Preference
- 认证: 复用 octo-web localStorage 裸键 (token/uid/name), embed 模式走 syncMatterAuth()
- 路由: hash-based (#/matters, #/matters/board, #/mailbox, #/matter/:id, #/project/:id, #/cards, #/automation, #/timeline)
- 创建表单: 邮件式 (领队→协作→主题→Brief→附件), status=backlog(草稿) / status=open(发车)
- 状态图标: Linear 风格 SVG 圆形 icon (statusIconSVG)
- 领队选择器: ownedBotsOnly (只显示自己的 bot + 自己)
- 协作选择器: humansOnly (只显示人类)
- 面包屑: 顶级页面不显示(setCrumbText("")), 仅 detail 页显示层级路径, mailbox 来源自动追踪(detailReferrer)
- Markdown: 统一渲染器 mdHTML(), 支持 heading/list/table/code fence/inline
- 偏好卡: Preference 页独立 .cards-toolbar CSS; 行展开有 chevron 指示器; 搜索清空走内存缓存
- 收件箱 detail: inboxDetailCache 按 matter ID 缓存已加载详情, 切换时即时展示
- 已读/未读: localStorage readIds Set(上限 2000), .unread 蓝点+粗体, markRead() 在选中时触发
- Tab 切换: paintMyInbox(my, preserveRight=true) 快路径只刷左侧列表, 右侧详情保留

## CSS token 体系

:root 变量遵循 Octo 设计稿, 关键层次:
- --text (100%) → --text-1 (80%) → --text-2 (66%) → --text-3 (50%) → --text-4 (38%) → --text-5 (28%)
- --hover = --surface-hover = rgba(28,28,35,.055)
- --fill-1 (2.5%) → --fill-2 (4.5%) → --fill-3 (6%)

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
