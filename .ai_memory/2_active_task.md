# 当前任务

## 目标

按照根目录 `功能优化方案.md` 对 NewAPI Tools 的仪表盘、风控中心和日志分析进行首轮落地优化，并在每轮完成后同步项目文档和记忆库。

## 已完成

- 数据库只读校验：已确认当前 MySQL 库包含 `logs/users/tokens/top_ups/channels/abilities/options` 等核心表，`logs` 约 38.98 万行；本地 `数据库地址*` 已加入 `.gitignore`，避免误提交连接信息。
- 用户管理：保留前一轮“批量注销不活跃用户”口径，已收紧为“未成功充值且从未调用过”，Go/Python/前端均已同步。
- 仪表盘：
  - Go/Python 新增 `/api/dashboard/snapshot`。
  - 前端首屏优先使用 snapshot，展示 `snapshot_time`、`cache_hit`、最新日志时间和滞后秒数。
  - 关键流量卡片可跳转到日志分析并携带当前周期参数。
- 风控中心：
  - Go/Python 风控排行补充 `risk_score`、`risk_level`、`risk_reasons/reasons`、`suggested_action` 和 `metrics`。
  - Go/Python 新增 `/api/risk/queue`，前端默认“风险评分”排序改走风险队列。
  - Go/Python 新增 `/api/risk/actions/batches` 与 `/{batch_id}/revert`，批量共享 IP 封禁改走服务端批次接口。
  - AI 复核输出新增 `evidence_summary`、`false_positive_risk`、`questions_for_admin`、`prompt_version`，前端已展示。
  - 共享 IP 区块已拆为 `frontend/src/components/risk/SharedIPAltAccountPanel.tsx`，并新增 `frontend/src/components/risk/types.ts` 承接 IP 风控类型。
  - 多用户共享 IP 已升级为“小号研判”视图：前端按用户聚集度、未封禁数量、令牌密度、请求量、首次出现跨度、充值线索计算风险等级；Go/Python `/api/ip/shared-users` 已补充未封禁数、用户已用额度、总调用数和成功充值次数。
  - 共享 IP 小号案件新增 AI 专项研判：Go/Python `/api/ai-ban/assess-shared-ip`、前端“AI研判”按钮和结果卡片已落地；Prompt 版本 `shared-ip-alt-account-v1`，后端对模型返回的分数、动作、误报风险和数组字段做归一化。
  - 小号风险案件 v1 已落地：Go/Python 新增 `/api/risk/alt-account/cases`、详情和 `/{case_id}/assess`；规则层实时生成共享 IP、30d 轮换账号池、邀请链、Token 轮换四类案件；前端新增 `AltAccountCasesPanel`。
- 日志分析：
  - Go `/api/analytics/process` 和 `/batch` 文案/响应语义改为“刷新统计缓存”。
  - Go/Python 新增 `/api/analytics/export-jobs`、状态查询和下载接口。
  - 前端导出改为创建任务、轮询、下载，并自动保存导出筛选条件。
- 文档：
  - 根目录 `功能优化方案.md` 已新增“当前落地状态”表，标注已完成和待深化项。
  - `docs/ai-risk-alt-account-requirements.md` 已整理 AI 小号风控需求，覆盖共享 IP 小号、24h 轮换账号池、邀请链、token/IP 轮换、定时 AI 复核、接口、存储、前端和验收标准。

## 验证

- `python -m py_compile backend-py\app\dashboard_routes.py backend-py\app\risk_monitoring_routes.py backend-py\app\risk_monitoring_service.py backend-py\app\log_analytics_routes.py backend-py\app\cache_manager.py backend-py\app\user_management_service.py backend-py\app\user_management_routes.py`
- `go test ./...`（`backend/`）
- `npm run build`（`frontend/`）
- 2026-05-17 共享 IP AI 研判真实效果检验：只读读取真实 MySQL 24h 共享 IP 样本，使用 `https://newapi.youkies.space/` 可见模型 `「按量」gpt-5.5` 调用成功；选中脱敏 IP 样本 15 用户/15 未充值/首次跨度约 4171 秒，AI 给出高风险但建议先复核，验证出 schema 归一化需求并已加固。
- 2026-05-17 小号风险案件 v1 真实效果检验：30d live rules 生成 127 个候选案件；24h 共享 IP 样本为 15 用户/15 未充值/15 token/18 请求，通用案件 AI 研判返回 86 分、`review`、置信度 0.78，耗时约 73 秒。验证时发现 PowerShell 非 UTF-8 请求体会把中文模型 ID 传成问号，浏览器和 Go 后端应保持 UTF-8 JSON。

## 后续深化

- 风控中心 `RealtimeRanking.tsx` 已完成共享 IP 区块和小号案件面板拆分；仍需继续拆风险队列、AI 面板、审计记录和 hooks。
- 共享 IP AI 结果卡片和通用小号案件 AI 结果仍可继续组件化，后续可拆为 `AltAccountAIResultCard` 或放入 `IPCaseList` 体系。
- 小号风控下一阶段应按 `docs/ai-risk-alt-account-requirements.md` 实现案件持久化表、AI 研判历史、处置状态、管理员反馈闭环和后台定时扫描。
- 真正的 `api_tools_usage_rollup_hourly/daily` 增量聚合表仍未落地。
- 日志分析的失败率、耗时、消费突增异常视图仍未新增。
- 本轮尚未启动本地服务做浏览器交互验收。
