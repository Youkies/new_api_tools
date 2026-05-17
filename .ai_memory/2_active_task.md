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
- 日志分析：
  - Go `/api/analytics/process` 和 `/batch` 文案/响应语义改为“刷新统计缓存”。
  - Go/Python 新增 `/api/analytics/export-jobs`、状态查询和下载接口。
  - 前端导出改为创建任务、轮询、下载，并自动保存导出筛选条件。
- 文档：
  - 根目录 `功能优化方案.md` 已新增“当前落地状态”表，标注已完成和待深化项。

## 验证

- `python -m py_compile backend-py\app\dashboard_routes.py backend-py\app\risk_monitoring_routes.py backend-py\app\risk_monitoring_service.py backend-py\app\log_analytics_routes.py backend-py\app\cache_manager.py backend-py\app\user_management_service.py backend-py\app\user_management_routes.py`
- `go test ./...`（`backend/`）
- `npm run build`（`frontend/`）

## 后续深化

- 风控中心 `RealtimeRanking.tsx` 仍需继续拆分到 `frontend/src/components/risk/` 和 hooks。
- 真正的 `api_tools_usage_rollup_hourly/daily` 增量聚合表仍未落地。
- 日志分析的失败率、耗时、消费突增异常视图仍未新增。
- 本轮尚未启动本地服务做浏览器交互验收。
