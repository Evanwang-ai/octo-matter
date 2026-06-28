# scripts/
> L2 | 父级: /CLAUDE.md

验证与测试脚本: 冒烟测试、CLI 集成测试、巡检、UI 扫描、Multica 重演。

## 成员清单

v2-smoke.sh:        v2 冒烟测试 (API 端到端, 涵盖状态机/sub-matter/doorbell/权限守卫)
v2-cli-cases.sh:    CLI 集成测试 (octo-cli 真实 bot token, 委托三拍/圈点打回/撒网/epoch 围栏/完成限权)
v2-patrol.sh:       巡检探针 (扫描运行时异常: 幽灵铃/僵死 matter/outbox 积压)
v2-ui-sweep.mjs:    UI 端到端扫描 (Puppeteer/CDP, 验证页面渲染 + 控制台零 error)
v2-mailbox-mymatters-smoke.mjs: 收件箱 / 全局事项 live UI smoke (List/Board/legacy hash, optional system-letter fixture)
multica-replay.py:  Multica issue 重演 (导入真实 issue → 创建 matter → 验证闭环)

## 使用方式

```bash
# 冒烟 (需要服务运行中)
./scripts/v2-smoke.sh

# CLI 集成 (需要 octo-cli + bot token)
BOT_UID=27A8InGz... ./scripts/v2-cli-cases.sh

# 巡检
./scripts/v2-patrol.sh

# 收件箱 / 全局事项 UI 冒烟
RUN_LABEL=local node scripts/v2-mailbox-mymatters-smoke.mjs

# Multica 重演 (需要 PAT + workspace_id)
python3 scripts/multica-replay.py
```

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
