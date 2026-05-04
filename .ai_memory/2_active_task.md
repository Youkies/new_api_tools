# 当前任务

## 目标

把 NewAPI 日志导出助手的思路融合进工具的成本核算：支持从上游 NewAPI 手动或定时同步消费日志，并把上游真实成本与本站本地日志成本合并。

## 已完成

- Go 后端新增上游日志同步服务，持久化配置到 `api_tools_upstream_log_sync_config`，导入上游日志到 `api_tools_upstream_logs`。
- Go `/api/cost` 新增：
  - `GET /api/cost/upstream-sync/config`
  - `POST /api/cost/upstream-sync/config`
  - `POST /api/cost/upstream-sync/run`
- Go 启动后新增后台定时同步任务；启用后按配置的 `interval_minutes`、`lookback_minutes`、`overlap_minutes` 执行。
- 成本核算改为优先使用已匹配的上游导入成本，未匹配部分继续按本地成本规则估算。
- 根据 `参考日志/` 的真实样本验证：本站与上游 `Request ID` 精确匹配率为 0%，因此匹配主策略改为“一对一输入 tokens + 输出 tokens + 时间窗口”，`Request ID` 仅保留为高置信兜底和诊断统计。
- 前端成本核算页新增“上游日志同步”配置区、手动同步按钮、匹配率和导入/规则成本拆分展示。
- Go 后端新增 `POST /api/cost/upstream-sync/upload`，支持 userscript 上传已导出的上游原始日志，并保存 `source_url`/`source_name` 后立即执行一对一匹配。
- `NewAPI 日志导出助手-1.2.2.user.js` 已改造为 1.2.3：导出后可填写 NewAPI Tools 地址和 `API_KEY`/Bearer JWT，上传日志到 tools 后台自动匹配。
- Python 兼容后端补齐配置接口和成本汇总对 `api_tools_upstream_logs.local_log_id` 的兼容读取；实际上游抓取同步仍以 Go 后端为正式实现。

## 验证结果

- `go test ./...`（`backend/`）通过。
- `python -m py_compile backend-py/app/cost_accounting_service.py backend-py/app/cost_accounting_routes.py` 通过。
- `npm run build`（`frontend/`）通过；仍有既有 CSS minify/chunk size warning。

## 注意

- `参考日志/` 和 `NewAPI 日志导出助手-1.2.2.user.js` 是用户提供/参考文件，当前未纳入提交范围。
- `参考日志/` 是用户提供的样本文件，不纳入提交范围。
- 当前已有本地提交 `ffa6174 新增上游日志同步成本整合`，本轮 userscript 上传适配尚未提交或 push。
