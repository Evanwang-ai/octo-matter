# internal/config/
> L2 | 父级: /CLAUDE.md

环境变量配置: 数据库连接、octo-server URL、通知 token、LLM key、引擎参数。

## 成员清单

config.go:  Config 结构体 + Load() 从环境变量读取, 含验证

## 关键环境变量

- `DATABASE_DSN` / `MYSQL_*` — MySQL 连接
- `OCTO_IM_URL` — octo-server 基址 (鉴权+通知)
- `NOTIFY_INTERNAL_TOKEN` — 内部通知鉴权 token
- `LLM_API_KEY` — LLM 提供商 key (可空, 缺失降级)
- `MATTER_DISPATCH_INTERVAL` — outbox 扫描间隔 (默认 3s)
- `MATTER_WATCHDOG_INTERVAL` — 看门狗间隔 (默认 60s)
- `MATTER_REDELIVER_AFTER` — doorbell 重投延迟 (默认 10m)
- `PORT` — HTTP 监听端口

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
