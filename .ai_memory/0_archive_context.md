## 2026-04-28 风控系统优化

用户要求将“多令牌共用 IP”风险视图优化为“多用户共用 IP”，同时加强 AI 风控但保持谨慎封禁。当前方向是：扩大审查候选范围，默认把高风险用户放入待处理复核区，由管理员确认；仅在特征极明显、评分和置信度都达到极高阈值，并且配置显式允许时才自动执行封禁。系统开始记录管理员对待复核项的处理反馈，作为后续优化风控规则和 AI 提示词的学习数据。

## 2026-05-27 小号案件工作台 v1 撤回

**Why**：5 月 17 日一天连发的「小号案件工作台 + AI 研判」v1（`0f16701` + `7ee8591`）线下使用后，用户判定**产品形态不符合预期**。方向（小号识别 + AI 辅助研判 + 统一工作台）本身是对的，所以这是**临时撤回等待重做**，不是放弃。

**回退范围决策**：

| commit | 主题 | 处理 | 理由 |
|---|---|---|---|
| `7ee8591` 统一小号案件工作台 | 小号工作台 UI | revert | 不符合预期，待重做 |
| `0f16701` 优化小号案件与 AI 研判 | 小号案件 + AI 研判 | revert | 不符合预期，待重做 |
| `da54a5b` 落地仪表盘风控日志优化 | 仪表盘 snapshot / 风险队列 / 批量动作批次 / AI 复核 schema / 日志导出任务化 | reapply | 第一波 revert 误伤，与小号案件工作台是不同方向，是基础设施级工程优化，必须保留 |
| `1b64b3a` 用户维度共享 IP 基础设施 | `GetSharedUserIPs` / `/api/ip/shared-users` | 保留 | 这是「按用户聚合」的查询接口，本身是对的方向，未来重做也要复用 |

**重做时可复用的基础设施**：
- 后端：`/api/ip/shared-users`、`/api/risk/queue`、`/api/risk/actions/batches` + `/revert`、`/api/ai-ban/pending-reviews` + `/resolve`、`/api/ai-ban/assess-shared-ip`、AI 复核 schema（`evidence_summary` / `false_positive_risk` / `questions_for_admin` / `prompt_version`）
- 前端：`RealtimeRanking.tsx` 仍包含风险评分排序、风险队列拉取、批量封禁批次化与撤销、AI 待审列表、AI 评估弹窗

**待用户后续明确**：v1 哪里不符合预期（数据视角错？UI 信息密度？AI 研判置信不够？还是案件分类不合理？）—— 这是下一版设计的入口。

**生产影响**：CI 仅构建 Go 后端（`Dockerfile` 是 All-in-One Go binary，`.github/workflows/build.yml` 的 path filter 不含 `backend-py/**`）。当前 main `a8e50df` push 后会重新构建 `:latest`。`backend-py/` 中残留的 380 行 0f16701 改动对生产无影响，但 Python 端 schema 比 Go 端少四个 AI 复核字段（前端字段 optional，不崩）。
