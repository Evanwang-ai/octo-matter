# internal/auth/
> L2 | 父级: /CLAUDE.md

鉴权中间件: 调用 octo-server 公开 API 验证 token, 注入 uid/space/bot 身份到 Gin context。

## 成员清单

middleware.go:       AuthMiddleware + SpaceMiddleware: token/bot 验证, X-Space-Id → /v1/space/:id 校验
space_verifier.go:   SpaceCreateVerifier: service 层可调用的 space create gate, 复用 /v1/space/:id token check
middleware_test.go:  鉴权中间件测试
space_verifier_test.go: SpaceCreateVerifier 测试

## 关键行为

- 人类 token: `token` header → POST octo-server /v1/auth/verify → uid, name, ownedBots, relatedUIDs=[self+owned bots]
- Bot token: `Authorization: Bearer` → POST octo-server /v1/auth/verify-bot → bot_uid, owner_uid, role=bot, relatedUIDs=[bot]
- SpaceMiddleware: X-Space-Id header 必须存在; `GET /v1/space/:id` 200 放行, 4xx→SPACE_FORBIDDEN, 5xx/网络→UPSTREAM
- SpaceCreateVerifier: 给 user-level service 手动验证目标 space; Mailbox convert 使用它, 不能偷用 spaceMW 上下文
- 配置: OCTO_IM_URL 环境变量

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
