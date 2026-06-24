# internal/notification/
> L2 | 父级: /CLAUDE.md

通知投递层: doorbell 发送到 octo-server, 异步 worker 池, channel 消息发送。

## 成员清单

doorbell.go:          DoorbellSender 接口 + OctoNotifier 实现, POST /v1/internal/notify, bot 目标追加 matter UUID+读单命令
doorbell_test.go:     Doorbell 投递测试
notifier.go:          Notifier 接口 (系统通知抽象)
notifier_test.go:     Notifier 测试
octo_notifier.go:     OctoNotifier: HTTP 客户端封装, X-Internal-Token 鉴权, octo-server 内部 API
worker.go:            Worker: 有界协程池 (固定 buffer + N workers), Submit 非阻塞(满则丢弃+warn), Shutdown 等待排空
worker_test.go:       Worker 测试
channel_message.go:   Channel/Thread 消息发送 (homecoming 回源群)
templates.go:         通知模板 (doorbell 文案格式化)

## 投递链路

```
TransitionService.Apply()
  → EnqueueStandalone() 入队 outbox (事务内)
  → Engine.dispatchOnce() 扫描 outbox
  → DoorbellSender.SendDoorbell() HTTP POST
  → octo-server /v1/internal/notify
  → WuKongIM → Agent runtime
```

## 关键约束

- DoorbellSender 是同步阻塞的, 重试由 Engine 管理
- 投递失败 → Engine 递增 retry_count + next_retry_at
- 超过 MaxRetries(5) → 标记 dead + 升级通知主人
- homecoming doorbell 发到来源群, 不发个人通知
- bot 目标在文本后追加 matter UUID + `octo-cli api GET /api/v1/matters/<id>` 读单命令

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
