# internal/auth/
> L2 | 父级: /CLAUDE.md

鉴权中间件: 调用 octo-server 公开 API 验证 token, 注入 uid/space/bot 身份到 Gin context。

## 成员清单

middleware.go:       AuthMiddleware: token header→POST /v1/auth/verify, Authorization Bearer→POST /v1/auth/verify-bot; 解析 uid/name/isBot/ownedBots/relatedUIDs 注入 context
middleware_test.go:  鉴权中间件测试

## 关键行为

- 人类 token: `token` header → POST octo-server /v1/auth/verify → uid, name
- Bot token: `Authorization: Bearer` → POST octo-server /v1/auth/verify-bot → uid, name, isBot=true, ownedBots[], relatedUIDs[]
- relatedUIDs: bot 展开包含 owner uid (可见性), 但权限检查必须用真实 actorUID(callerUIDs[0])
- Space 校验: X-Space-Id header 必须存在, 否则 403
- 配置: OCTO_IM_URL 环境变量

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
