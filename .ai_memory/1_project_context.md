# 项目上下文

- 项目 `new_api_tools` 同时保留 Go 后端（`backend/`）和 Python 后端（`backend-py/`）；正式 Docker 根目录镜像倾向使用 Go 后端，Python 后端仍需保持接口兼容。
- 前端主要风险面板在 `frontend/src/components/RealtimeRanking.tsx`，用户分析弹窗在 `frontend/src/components/UserAnalysisDialog.tsx`。
- 风控系统已有 IP 监控、用户风险分析、AI 自动封禁配置、审查日志和白名单能力。
- 多用户共用 IP 风险标签使用 `MULTI_USER_SHARED_IP`，用于标识同一 IP 被多个不同用户使用的风险。
- AI 风控策略当前采用 `pending_review_first`：扫描频率和候选数量可以提高，但封禁默认进入待处理复核区；只有 `auto_execute_obvious_bans` 显式开启且极高风险时才自动执行。
- 成本核算功能新增独立 `成本核算` 页；Go/Python 后端都提供 `/api/cost/summary`、`/api/cost/rules`，配置持久化在工具自建的 `api_tools_channel_costs` 表中，按 `logs.channel_id + logs.model_name` 聚合，默认读取 NewAPI `channels.model_mapping` 解析真实上游模型；NewAPI `channels` 表无 `deleted_at`，成本查询不能硬编码该字段；模型重定向按 NewAPI 逻辑支持链式解析，并按“基础价格 × 渠道倍率”计算实际成本。
- 自动分组支持 `by_usage` 按累计消费升级模式：以 `users.used_quota / 500000` 换算美元，配置多档“消费达到 USD -> 目标分组”，扫描时命中最高门槛；只会从 `default` 或已配置的低档分组升级，不会降级或移动不在档位中的自定义分组。
- `by_usage` 默认开启充值判定 `usage_require_topup=true`：候选用户必须在 `top_ups` 表存在成功充值记录，成功状态按 `success`、`completed` 或 `1` 判断；如果要求充值但 `top_ups` 表不存在，应返回无候选用户。
- 自动分组目标分组列表不能只看 `users.group`；需要合并 NewAPI 的 `options.GroupRatio`、`UserUsableGroups`、`GroupGroupRatio`、`group_ratio_setting.group_special_usable_group`、`AutoGroups`，以及 `abilities.group`、`channels.group`，这样没有用户但已在倍率/特殊倍率中配置的分组也能被选择。
- 手动分组迁移支持先测试再执行：`/api/auto-group/batch-move` 使用 `dry_run=true` 只返回预览，不修改用户；正式迁移写入 `batch_id`，撤销整批迁移使用 `/api/auto-group/revert-batch`。自动分组日志需保留 `batch_id`、`reverted_at`、`revert_log_id`、`revert_of` 元数据，避免重复或错误撤销。
- Go 正式后端已补齐性能基础设施：前端使用路由级懒加载；Go `/api/system/scale` 基于用户数、日志估算和 24h 请求量返回刷新建议；`/api/system/warmup-status` 反映后台热缓存预热状态；模型状态使用批量 SQL 按 `model_name + slot` 聚合；风控排行榜、模型状态和 IP 列表支持 `no_cache=true` 真刷新。
- Go 正式后端已支持自动分组后台定时扫描：启动后按 `enabled`、`auto_scan_enabled`、`scan_interval_minutes` 静默执行 `RunScan(false)`，与前端“自动扫描”开关语义一致。
- 日志分析页已支持 `/api/analytics/export` 原始日志导出；Go/Python 后端都提供同名接口，按时间、类型、模型、用户、令牌、渠道、分组、Request ID 等条件从 `logs` 表直接流式导出 CSV/JSON，避免前端逐页请求 NewAPI `/api/log` 导致大量请求中断。
- 成本核算已接入上游日志同步设计：Go 正式后端通过 `/api/cost/upstream-sync/*` 配置、手动同步和后台定时同步上游 NewAPI 消费日志，导入到 `api_tools_upstream_logs`；成本汇总只使用已经一对一匹配到本地 `logs.id` 的上游成本，匹配主策略为“输入 tokens + 输出 tokens + 时间窗口”，`Request ID` 仅作为高置信兜底和诊断统计。参考样本显示本站与 5 个上游的 `Request ID` 精确匹配率为 0%，60 秒 token/time 窗口可匹配约 88%-94%。
- 上游日志也可由 userscript 上传：Go `/api/cost/upstream-sync/upload` 接收 `source_url`、`source_name` 和原始 `logs` 数组，写入 `api_tools_upstream_logs` 后立即匹配；改造后的 `NewAPI 日志导出助手` 在上游页面复用浏览器登录态拉 `/api/log/` 或 `/api/log/self/`，再用 NewAPI Tools 的 `X-API-Key` 或 Bearer JWT 跨域上传。
- 用户确认偏好：功能完成并验证通过后，默认自动提交并 push 当前分支；提交前仍需检查 `git status` 和 diff 范围，避免带入无关改动。
- 常用验证命令：`go test ./...`（在 `backend/`）、`python -m py_compile ...`、`npm run build`（在 `frontend/`）。
