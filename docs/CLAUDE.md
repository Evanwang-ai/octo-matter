# docs/
> L2 | 父级: /CLAUDE.md

Agent 文档层: 操作手册 + API 契约 + 协作模式指南。部分文件通过 HTTP 公开服务 (无鉴权)。

## 成员清单

SKILL.md:           Agent 操作手册, GET /skill.md 公开服务。铁律5条 + 门铃处理 + 单兵循环 + feedback 应对 + 领队协议 + 协作模式路由表
v2-api.md:          v2 完整 API 契约: 状态机/转换守卫/请求响应格式/新字段
AGENT_SETUP.md:     Agent 接入指南
embed.go:           //go:embed 嵌入 SKILL.md + modes/ 目录供 router 服务

## modes/ — 协作模式指南

六个独立文件, GET /modes/:name 公开服务, Cadmus 式按需注入 (runtime 只加载对应模式):

modes/solo.md:        单干模式 — 领队独自完成
modes/roundtable.md:  圆桌模式 — 互见讨论, 主 timeline @-mention, 领队收束
modes/critic.md:      审核模式 — 生成→验证, 主 timeline 接力, 最多 N 轮
modes/pipeline.md:    流水线模式 — A→B→C 链式, 主 timeline 接力
modes/split.md:       分治模式 — 互盲, sub-matter 隔离, 领队合并
modes/swarm.md:       竞选模式 — 互盲同题, sub-matter 隔离, 领队择优

## 两种信息拓扑

| 拓扑 | 模式 | 载体 |
|------|------|------|
| 互见 (主 timeline) | roundtable, critic, pipeline | @-mention 在主 timeline 中对话 |
| 互盲 (sub-matter) | split, swarm | 每个参与者在独立 sub-matter 中工作 |

## SKILL.md 领队协议 (§4) 核心循环

```
醒来 → 读 tree + timeline → 读自己上次计划笔记 →
想: 有什么变了? 计划还对吗? → 行动 → 写笔记 → 走人
```

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
