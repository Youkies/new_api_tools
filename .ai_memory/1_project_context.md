# 项目上下文

- 项目 `new_api_tools` 同时保留 Go 后端（`backend/`）和 Python 后端（`backend-py/`）；正式 Docker 根目录镜像倾向使用 Go 后端，Python 后端仍需保持接口兼容。
- 前端主要风险面板在 `frontend/src/components/RealtimeRanking.tsx`，用户分析弹窗在 `frontend/src/components/UserAnalysisDialog.tsx`。
- 风控系统已有 IP 监控、用户风险分析、AI 自动封禁配置、审查日志和白名单能力。
- 多用户共用 IP 风险标签使用 `MULTI_USER_SHARED_IP`，用于标识同一 IP 被多个不同用户使用的风险。
- AI 风控策略当前采用 `pending_review_first`：扫描频率和候选数量可以提高，但封禁默认进入待处理复核区；只有 `auto_execute_obvious_bans` 显式开启且极高风险时才自动执行。
- 常用验证命令：`go test ./...`（在 `backend/`）、`python -m py_compile ...`、`npm run build`（在 `frontend/`）。
