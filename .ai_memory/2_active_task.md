# 当前任务

## 目标

按照根目录 `功能优化方案.md` 对 NewAPI Tools 的仪表盘、风控中心和日志分析进行首轮落地优化，并在每轮完成后同步项目文档和记忆库。

## 已完成

- 数据库只读校验：已确认当前 MySQL 库包含 `logs/users/tokens/top_ups/channels/abilities/options` 等核心表，`logs` 约 38.98 万行；本地 `数据库地址*` 已加入 `.gitignore`，避免误提交连接信息。
- 用户管理：保留前一轮“批量注销不活跃用户”口径，已收紧为“未成功充值且从未调用过”，Go/Python/前端均已同步。
  - 2026-05-17 修复 Go `/api/users` 未应用 `inactive/very_inactive` 活跃度筛选的问题；列表现在按 7d/30d 请求窗口筛选，并返回准确 `activity_level` 与 `last_request_time`。
  - 2026-05-19 新增已注销账号恢复入口：Go/Python 新增 `/api/users/soft-deleted/search`、`/api/users/soft-deleted/restore`，前端用户管理页支持按注册邮箱检索已注销账号并恢复，默认同步恢复该账号已软删除的 token。
- 仪表盘：
  - Go/Python 新增 `/api/dashboard/snapshot`。
  - 前端首屏优先使用 snapshot，展示 `snapshot_time`、`cache_hit`、最新日志时间和滞后秒数。
  - 关键流量卡片可跳转到日志分析并携带当前周期参数。
- 风控中心：
  - Go/Python 风控排行补充 `risk_score`、`risk_level`、`risk_reasons/reasons`、`suggested_action` 和 `metrics`。
  - Go/Python 新增 `/api/risk/queue`，前端默认“风险评分”排序改走风险队列。
  - Go/Python 新增 `/api/risk/actions/batches` 与 `/{batch_id}/revert`，批量共享 IP 封禁改走服务端批次接口。
  - AI 复核输出新增 `evidence_summary`、`false_positive_risk`、`questions_for_admin`、`prompt_version`，前端已展示。
  - 多用户共享 IP 已升级为小号证据链的一部分：前端按用户聚集度、未封禁数量、令牌密度、请求量、首次出现跨度、充值线索计算风险等级；Go/Python `/api/ip/shared-users` 已补充未封禁数、用户已用额度、总调用数和成功充值次数。
  - 共享 IP 小号案件新增 AI 兼容研判：Go/Python `/api/ai-ban/assess-shared-ip` 保留；Prompt 版本 `shared-ip-alt-account-v1`，后端对模型返回的分数、动作、误报风险和数组字段做归一化。
  - ~~小号风险案件 v1 已落地并收敛为统一工作台~~（**2026-05-27 已 revert，等待重做**）：原方案在 Go/Python 新增 `/api/risk/alt-account/cases`、详情和 `/{case_id}/assess`；规则层实时生成共享 IP、30d 轮换账号池、邀请链、Token 轮换四类案件；前端 `AltAccountCasesPanel`。v1 实现不符合预期，方向不变，需重新设计后再做。保留的基础设施可复用：`/api/ip/shared-users`（用户维度查询）、`/api/risk/queue`（风险队列）、`/api/risk/actions/batches`（批量动作可撤销）、AI 复核 schema（evidence_summary/false_positive_risk/questions_for_admin/prompt_version）。
- 日志分析：
  - Go `/api/analytics/process` 和 `/batch` 文案/响应语义改为“刷新统计缓存”。
  - Go/Python 新增 `/api/analytics/export-jobs`、状态查询和下载接口。
  - 前端导出改为创建任务、轮询、下载，并自动保存导出筛选条件。
- 运营预警：
  - Go/Python 新增 `/api/operations/alerts` 和 `/api/operations/users/{user_id}/detail`。
  - 前端新增独立 `运营预警` 页面和导航入口，覆盖高价值用户停用、充值断档、新充值未激活、付费用户体验异常、支付状态异常。
  - 用户详情弹窗直接展示注册邮箱并支持复制，用于管理员人工联系；收入/毛利异常未启用，等待上游真实价格系统。
- 文档：
  - 根目录 `功能优化方案.md` 已新增“当前落地状态”表，标注已完成和待深化项。
  - `docs/ai-risk-alt-account-requirements.md` 已整理 AI 小号风控需求，覆盖共享 IP 小号、24h 轮换账号池、邀请链、token/IP 轮换、定时 AI 复核、接口、存储、前端和验收标准。
  - 根目录 `功能优化方案.md` 已补充 `运营预警` 专项说明和 v1 边界。

## 验证

- `python -m py_compile backend-py\app\dashboard_routes.py backend-py\app\risk_monitoring_routes.py backend-py\app\risk_monitoring_service.py backend-py\app\log_analytics_routes.py backend-py\app\cache_manager.py backend-py\app\user_management_service.py backend-py\app\user_management_routes.py`
- `go test ./...`（`backend/`）
- `npm run build`（`frontend/`）
- 2026-05-19 注销账号恢复功能验证：`go test ./...`（`backend/`）、`python -m py_compile backend-py\app\user_management_service.py backend-py\app\user_management_routes.py`、`npm run build`（`frontend/`）均通过；前端构建仍只有既有 CSS minify/chunk size 警告；Vite 本地服务 `http://127.0.0.1:5173` 已启动并返回 HTTP 200。
- 2026-05-17 用户管理筛选修复验证：`go test ./...`、`python -m py_compile backend-py\app\user_management_service.py backend-py\app\user_management_routes.py`、`npm run build` 均通过；真实库只读计数验证 `active/inactive/very_inactive/never` 均可按 SQL 条件区分。
- 2026-05-17 共享 IP AI 研判真实效果检验：只读读取真实 MySQL 24h 共享 IP 样本，使用 `https://newapi.youkies.space/` 可见模型 `「按量」gpt-5.5` 调用成功；选中高风险 IP 样本 15 用户/15 未充值/首次跨度约 4171 秒，AI 给出高风险但建议先复核，验证出 schema 归一化需求并已加固；当前管理员风控口径为完整 IP 可见且可发送给 AI。
- 2026-05-17 小号风险案件 v1 真实效果检验：30d live rules 生成 127 个候选案件；24h 共享 IP 样本为 15 用户/15 未充值/15 token/18 请求，通用案件 AI 研判返回 86 分、`review`、置信度 0.78，耗时约 73 秒。验证时发现 PowerShell 非 UTF-8 请求体会把中文模型 ID 传成问号，浏览器和 Go 后端应保持 UTF-8 JSON。

## 后续深化

- 新讨论方向：运营预警/运营健康预案。用户关注“高消费用户突然不使用、不充值”这类运营异常，需要先形成产品预案；用户倾向单独做一个页面，建议定位为“运营预警/运营健康”独立页面，并与仪表盘异常入口、日志分析、用户画像和支付异常联动，而不是并入自动封禁。收入/毛利异常暂不纳入 v1，因为当前没有接入上游真实价格系统，避免输出不可靠的成本/利润判断。运营预警页点开用户详情时应直接展示注册邮箱，便于管理员联系；注意邮箱只做管理员详情字段，不发送给外部 AI、不默认导出。
- 风控中心 `RealtimeRanking.tsx` 已完成共享 IP 证据链并入小号案件工作台；仍需继续拆风险队列、AI 面板、审计记录和 hooks。
- ~~小号案件 AI 结果仍可继续组件化，后续可拆为 `AltAccountAIResultCard` 或放入 `AltAccountCaseList` 体系。~~（与小号案件工作台一并搁置，待重做时一并考虑）
- ~~小号风控下一阶段应按 `docs/ai-risk-alt-account-requirements.md` 实现案件持久化表、AI 研判历史、处置状态、管理员反馈闭环和后台定时扫描。~~（v1 已 revert，下一版重新设计需求文档可能也需要重写）
- **下一步：重做「小号风险案件 + AI 研判」**。v1 不符合预期的具体点尚未明确记录；新方案需先澄清 v1 哪里不对，再决定数据结构、规则层、UI 形态。
- 运营预警下一阶段可将“已处理/忽略/观察中”从前端 localStorage 升级为服务端持久化，并增加阈值配置页。
- 真正的 `api_tools_usage_rollup_hourly/daily` 增量聚合表仍未落地。
- 日志分析的失败率、耗时、消费突增异常视图仍未新增。
- 2026-05-19 本轮已启动 Vite 本地服务并完成首页 HTTP 200 检查；尚未连接真实后端账号做恢复接口的 live 数据库操作。
