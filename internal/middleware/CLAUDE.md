# internal/middleware/
> L2 | 父级: /CLAUDE.md

通用 HTTP 中间件: 请求 ID 注入 + 限流。

## 成员清单

request_id.go:       X-Request-Id 中间件, 无则生成 UUID
request_id_test.go:  请求 ID 测试
rate_limit.go:       令牌桶限流中间件
rate_limit_test.go:  限流测试

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
