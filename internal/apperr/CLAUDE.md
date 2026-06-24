# internal/apperr/
> L2 | 父级: /CLAUDE.md

业务错误类型: 统一错误码 + HTTP 状态码映射。

## 成员清单

errors.go:  AppError 结构体 (Code string, HTTPStatus int, Message string), 预定义错误常量 (NOT_FOUND, FORBIDDEN, VERSION_CONFLICT, EPOCH_STALE, BOT_ALREADY_ADDED, LLM_NOT_CONFIGURED 等)

## 关键错误码

- `NOT_FOUND` (404) — matter/project/schedule 不存在
- `FORBIDDEN` (403) — 权限不足
- `VERSION_CONFLICT` (409) — CAS 乐观锁冲突
- `EPOCH_STALE` (409) — bot assignment_epoch 过期, bot 必须停止
- `BOT_ALREADY_ADDED` (409) — bot 已在 matter_bot_resources 中
- `LLM_NOT_CONFIGURED` (501) — LLM key 未配置

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
