# Octo Matter — Human×Agent 协作工作台的事项引擎

Go 1.25 + Gin + gocraft/dbr/v2 (MySQL 8) + google/uuid + go-playground/validator/v10

<directory>
cmd/          - 入口与依赖装配 (2文件: main.go, timeline_tx.go)
internal/     - 业务核心 (12子包: handler, service, repository, model, notification, auth, config, i18n, middleware, llm, octoim, apperr, webui)
docs/         - Agent 操作手册 + API 契约 + 协作模式指南 (SKILL.md, v2-api.md, modes/)
migrations/   - 数据库迁移 001-021 (显式嵌入, 新增必须手动注册 embed.go)
scripts/      - 冒烟测试与验证脚本 (v2-smoke.sh, v2-cli-cases.sh, v2-patrol.sh 等)
</directory>

<config>
docker-compose.yaml  - 本地 MySQL + 服务编排
Dockerfile           - 多阶段构建, 最终 distroless
go.mod / go.sum      - 依赖锁定
.github/             - PR 模板
</config>

## 分层架构

```
HTTP 请求
  ↓
handler/     Gin 路由, 请求绑定, 响应格式化
  ↓
service/     业务逻辑, 权限守卫, 状态机
  ↓
repository/  dbr 查询 (参数化; 禁止拼接 SQL)
  ↓
MySQL 8      外键, 唯一索引, 21 个迁移文件
```

异步路径:
```
TransitionService.Apply()  在同一事务内入队 doorbell
  ↓
Engine.Start()  两个后台循环:
  • dispatchOnce()   outbox 投递 → notification/DoorbellSender
  • watchdogOnce()   僵死检测 → revive/block 升级
  ↓
notification/Worker  有界协程池
  ↓
OctoNotifier.SendDoorbell()  HTTP POST → octo-server /v1/internal/notify
```

## 核心不变量

1. **空间隔离**: 每条查询必须 WHERE space_id (X-Space-Id header), 缺失=跨租户泄漏
2. **状态机**: 所有转换走 TransitionService.Apply, 禁止直接写 matters.status
3. **事务性 Outbox**: doorbell 在转换同一事务内入队, Engine 负责投递/重试/死信
4. **Epoch 围栏**: bot 写入携带 assignment_epoch, 过期→409 EPOCH_STALE(bot 必须停止)
5. **CAS 锁**: 并发写入需 expected_version, 不匹配→409 VERSION_CONFLICT
6. **禁止自评**: bot 不能验收自己的工作(done), 只有人类可以
7. **父→done 守卫**: 所有非 cancelled 子任务必须已终结
8. **子任务创建限权**: 只有 leader/creator/人类协作者可以派 sub-matter, bot 协作者禁止
9. **dbr Only**: 禁止 GORM, 用 gocraft/dbr/v2 builder; 禁止字符串拼接用户输入
10. **UUID 应用层生成**: google/uuid, 不依赖数据库

## 六态状态机

```
backlog(草稿) → open(发车) → in_progress → review → done
                              ↕ blocked         ↗ (打回)
任何非终结态 → cancelled
```

- backlog 只能→open(发车, creator only) 或→cancelled
- 发车(backlog→open)触发 assigned doorbell
- 创建默认 backlog; cron/丝滑路径直接 open

## 权限模型: 树即权限

- **发起人(人)**: god-mode, 全树任意层级干预
- **领队(人)**: 可验收/取消/派活/改派/加人
- **领队(bot)**: 只能执行+向下派活, 不能验收/取消
- **协作(人)**: 可创建子任务/可验收
- **协作(bot)**: 只能执行, 不能创建子任务/不能验收
- **Bot 主人**: 只有主人能把自己的 bot 加进 matter(主人主权)

## 六种协作模式

| 模式 | 可见性 | 信息载体 | 领队职责 |
|------|--------|---------|---------|
| solo | — | 主 timeline | 自己干 |
| roundtable | 互见 | 主 timeline @-mention | 主持/追问/收束 |
| critic | 串行 | 主 timeline 接力 | 转交/判断通过或打回 |
| pipeline | 链式 | 主 timeline 接力 | 规划步骤/监控链条 |
| split | 互盲 | sub-matter 隔离 | 拆分边界/合并产出 |
| swarm | 互盲 | sub-matter 隔离 | 发题/评选最优 |

mode_config (JSON): roundtable={participants}, critic={generator,verifier,max_rounds}, pipeline={steps[]}

## 命令

```bash
go build ./...                              # 构建
go test ./...                               # 全量测试
go test ./internal/service/ -run TestV2 -v  # 指定测试
docker compose up -d                        # 本地 MySQL + 服务
scripts/v2-smoke.sh                         # 冒烟测试
scripts/v2-cli-cases.sh                     # CLI 集成测试
```

## 规则

- 代码、注释、commit message 用英文
- 每次 commit 前跑 `go build ./...` + `go test ./...`
- 禁止 GORM
- handler 禁止跳过 service 层直接访问 repository
- 新增迁移必须手动注册到 migrations/embed.go
- push 用 matter-test remote, 不是 origin

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
