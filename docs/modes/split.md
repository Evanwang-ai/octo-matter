# Split — 分头干模式

> **互盲，用 sub-matter 隔离。**
> 任务拆成互不相关的子块，各 agent 在自己的 sub-matter 里独立完成，互相看不到。
> Leader 最后在父 matter 里合并。

## 立场

Split 适用于任务可以干净分区的场景——各块之间没有依赖、不需要讨论。
每个 agent 只看到自己那块的 Brief，对其他块一无所知（互盲）。

互盲是刻意的——避免参与者互相影响，保证每块独立完整。

## Leader 流程

```
[1] 写计划笔记
    读 Brief → 读 agent-cards 名册 → 规划分区
    在父 matter timeline 写出计划：
    「模式：split / 分区：A=块1、B=块2、C=块3 / 下一步：创建子任务。」

[2] 创建子任务（所有子任务可同时创建）
    每个子任务的 description 只包含该分区的信息
    不要在子任务 description 里提及其他分区（互盲原则）

    octo-cli api POST /api/v1/matters --data '{
      "title":"[总任务] — 块 A: [块名]",
      "parent_matter_id":"<父id>",
      "step_id":"chunk-a", "step_order":1, "status":"open",
      "leader_uid":"<agent_a_uid>",
      "assignee_ids":["<agent_a_uid>"],
      "description":"## 你的分区\n[只写块 A 的信息]\n\n## 输出要求\n[格式]"}'

[3] 等待所有子任务交回
    子任务交回 → 门铃唤醒 → 读骨架判断是否全部收齐

    octo-cli api GET /api/v1/matters/<父id>/tree

    没收齐 → 更新计划笔记（"块 A 已回，等块 B/C"），继续等
    全收齐 → 进入 [4]

[4] 合并
    读取每个子任务 timeline 里的产出
    检查各块之间是否有冲突或重叠
    合并为一个完整的交付物，写入父 matter timeline
    如有冲突 → 标注并给出 Leader 的裁决
    → 置 review 交回
```

**保活**：等待子任务交回期间需定期 touch：
`octo-cli api POST /api/v1/matters/<父id>/touch`

## Brief 应包含

- 任务的整体描述
- 分区建议（可选——Leader 可以自己判断怎么分）
- 各分区的边界说明

## 禁止

- 子任务之间不得互相引用（互盲）
- 不得让子任务自己去读其他子任务的 timeline
- 不得在子任务 description 里泄露其他分区的信息
