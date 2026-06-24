# internal/octoim/
> L2 | 父级: /CLAUDE.md

Octo IM 客户端: 调用 octo-server API 查询 channel 成员、bot 信息等。

## 成员清单

client.go:       OctoIMClient: HTTP 客户端, 查询 channel 成员关系 (用于权限检查), 获取 bot 归属信息
client_test.go:  客户端测试

## 关键接口

- IsChannelMember(spaceID, channelID, uid) — 检查用户是否在 channel 内
- 配置: OCTO_IM_URL 环境变量

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
