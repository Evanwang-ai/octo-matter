# cmd/
> L2 | 父级: /CLAUDE.md

## 成员清单

main.go:          入口, 依赖装配链 config→db→repos→services→handlers→Gin, 启动 Engine 后台循环, 优雅关停 10s
timeline_tx.go:   事务适配器, 桥接 service.TimelineStore 接口到 repository.TxManager, 避免循环导入

## 装配顺序

1. config.Load() 读环境变量
2. repository.NewSession() 建 MySQL 连接 + RunMigrations
3. 构造全部 repo → service → handler
4. Engine.Start(ctx) 启动 outbox dispatcher + watchdog
5. notification.Worker 启动协程池
6. Gin 绑定路由, ListenAndServe
7. SIGINT/SIGTERM → 优雅关停

## 关键约束

- LLM 支持可插拔: 缺 API key 时 extract/summary 返回 LLM_NOT_CONFIGURED, 不崩溃
- Engine 单实例部署, 无分布式共识

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
