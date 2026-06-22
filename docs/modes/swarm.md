# Swarm — 撒网竞选模式

> **互盲，用 sub-matter 隔离。**
> 同一个任务交给多个 agent 独立完成（互盲），Leader 从中选最优。
> 用多路冗余换质量。

## 立场

Swarm 的逻辑是「不知道谁最擅长，就都试一遍」。
每个 agent 收到完全相同的 Brief，独立产出，互不可见。
Leader 最后当裁判——选出最优，或综合最优部分。

与 Split 的区别：Split 是不同题（每块不一样），Swarm 是同题（Brief 完全相同）。

## Leader 流程

```
[1] 写计划笔记
    读 Brief → 读 agent-cards 名册 → 确定参与者
    在父 matter timeline 写出计划：
    「模式：swarm / 参与者：A、B、C / 评选标准：[来自 Brief]
      下一步：创建子任务，Brief 完全相同。」

[2] 创建子任务（所有子任务同时创建，Brief 完全相同）
    不要告诉参与者「还有其他人在做同样的事」

    octo-cli api POST /api/v1/matters --data '{
      "title":"[总任务] — 方案 1",
      "parent_matter_id":"<父id>",
      "step_id":"s1", "step_order":1, "status":"open",
      "leader_uid":"<agent_a_uid>",
      "assignee_ids":["<agent_a_uid>"],
      "description":"[父 matter Brief 原文，完全相同]"}'

[3] 等待所有子任务交回
    子任务交回 → 门铃唤醒 → 读骨架判断是否全部收齐

    octo-cli api GET /api/v1/matters/<父id>/tree

    没收齐 → 更新计划笔记，继续等
    全收齐 → 进入 [4]

[4] 评选
    逐个阅读每份产出
    按 Brief 里的 Goal/追踪目标作为评选标准
    在父 matter timeline 写出评选理由：
    「s1 在 X 维度最强，s2 在 Y 维度更好，综合选 s1。」

[5] 交回
    将选中的产出（或综合版）写入父 matter timeline
    → 置 review 交回
```

**保活**：等待子任务交回期间需定期 touch：
`octo-cli api POST /api/v1/matters/<父id>/touch`

## Brief 应包含

- 任务描述（所有参与者收到完全相同的内容）
- 评选标准（什么算"好"——准确性/创意/深度/实用性）
- 可选：期望产出的格式

## 禁止

- 子任务之间不得互相看到（互盲）
- Leader 不得在评选前给任何子任务额外提示
- 评选必须有理由，不得「随便选一个」
