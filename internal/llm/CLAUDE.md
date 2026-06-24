# internal/llm/
> L2 | 父级: /CLAUDE.md

LLM 客户端: 多提供商适配 (OpenAI / Anthropic), prompt 模板嵌入。可插拔, 缺 key 不崩溃。

## 成员清单

client.go:                  LLM 客户端统一接口 + 工厂函数
client_test.go:             客户端测试
openai_official.go:         OpenAI 官方 SDK 适配器
openai_official_test.go:    OpenAI 测试
anthropic_official.go:      Anthropic 官方 SDK 适配器
anthropic_official_test.go: Anthropic 测试
prompts/embed.go:           //go:embed 嵌入 prompt 模板 (.md 文件)
prompts/extract_matter.md:  Matter 字段抽取 prompt
prompts/extract_progress.md: 进度抽取 prompt
promptstore/store.go:       PromptStore: 按名称加载嵌入的 prompt 模板
promptstore/embed.go:       嵌入注册
promptstore/embed_test.go:  嵌入测试

## 关键约束

- LLM key 缺失时, service 层返回 LLM_NOT_CONFIGURED 错误, 不 panic
- 客户端选择: 根据 key 前缀自动判断 openai/anthropic
- prompt 模板编译时嵌入, 不运行时读文件

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
