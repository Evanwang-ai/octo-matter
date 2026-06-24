# internal/i18n/
> L2 | 父级: /CLAUDE.md

国际化: Accept-Language 中间件 + go-i18n bundle + doorbell/错误消息本地化。

## 成员清单

bundle.go:      i18n.Bundle 初始化, 加载 zh-CN / en 翻译
ctx.go:         从 Gin context 取语言偏好
keys.go:        所有 i18n key 常量 (doorbell 文案, 错误消息, 状态名)
lang.go:        语言检测逻辑
middleware.go:   Accept-Language 解析中间件
respond.go:     Localize() 函数: key + params → 本地化字符串
i18n_test.go:   i18n 测试

## 关键 key 类别

- `doorbell.*` — 门铃文案 (assigned, handed_back, feedback, blocked, done, reflect, revive, schedule, context_added)
- `error.*` — 业务错误消息
- `status.*` — 状态显示名

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
