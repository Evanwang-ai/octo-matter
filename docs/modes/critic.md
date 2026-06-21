# Critic — 生成-验证模式

> 一个 agent 生成，另一个 agent 验证。验证方有否决权。
> 核心原则：自查不算验证。

## 立场

Critic 模式的价值在于**独立视角**。生成方和验证方必须是不同的 agent，
因为同一个 agent 检查自己的工作，只会看到自己想看到的。

## Leader 流程

```
[1] 派生成子任务
    octo-cli api POST /api/v1/matters --data '{
      "title":"[任务名] — 生成",
      "parent_matter_id":"<父id>",
      "step_id":"generate", "step_order":1,
      "leader_uid":"<生成方uid>",
      "description":"[Brief 原文 + 输出要求]"
    }'

[2] 生成方交回（门铃通知你）→ 读取产出
    octo-cli api GET /api/v1/matters/<子id>/timeline

[3] 派验证子任务（leader_uid 必须 ≠ 生成方）
    octo-cli api POST /api/v1/matters --data '{
      "title":"[任务名] — 验证",
      "parent_matter_id":"<父id>",
      "step_id":"verify", "step_order":2,
      "leader_uid":"<验证方uid>",
      "description":"## 待审内容\n[粘贴生成方产出]\n\n## 审计标准\n[见下方模板]"
    }'

[4] 验证方交回 → 读取审计结果
    通过 → 汇总两方产出写入父单 timeline，置 review 交回
    未通过 → 回到 [1]，description 追加验证方的反馈作为约束
    （最多重做 2 轮，第 3 轮强制交回并标注未通过的点）
```

**保活**：等待生成/验证方交回期间需定期 touch：
`octo-cli api POST /api/v1/matters/<父id>/touch`

## 审计标准（验证方必读）

验证方收到的 description 里必须包含明确的 benchmark。如果 Brief 里没有写，
Leader 需要自己补充。Benchmark 格式：

```
## 审计标准
1. [维度1]：[什么算通过，什么算不通过]
2. [维度2]：...
3. [维度3]：...

## 审计要求
- 逐条对照标准，给出 ✓/✗ 和理由
- 不接受"整体还行"这种模糊评价
- 发现问题时，给出具体的修改建议（不只是指出问题）
```

## 验证方行为规范

1. **独立判断**：不要因为生成方是"同事"就放水
2. **具体指证**：每个 ✗ 都要引用原文具体位置 + 说明为什么不合格
3. **建设性**：指出问题的同时给出修改方向
4. **结论明确**：最后一行必须是 `**结论：通过**` 或 `**结论：未通过，原因：XXX**`

## Brief 应包含

- 生成任务的完整描述
- 审计标准（什么算"好"）—— 如果用户没写，Leader 需要基于任务性质自行拟定
- 可选：验证的重点维度（准确性/完整性/可读性/安全性）

## 铁律

- 生成方 ≠ 验证方（同一个 agent 不得同时担任两个角色）
- 没派验证子单就汇总交回 = 违约
- 验证方人选从 agent-cards 名片里挑，优先选有相关能力声明的
