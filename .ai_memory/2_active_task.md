# 当前任务

## 目标

先在本地完成 NewAPI 多站点 CSV 日志匹配核算器，验证匹配口径、成本倍率和按次不亏价；随后迁入 NewAPI Tools，让本站日志从数据库读取，上游日志手动上传分析。

## 已完成

- 已将 Go/Python 后端、前端成本核算页恢复到 `a749d55`（日志分析导出完成后、上游成本整合开始前）的状态。
- 已删除 `backend/internal/service/upstream_log_sync.go`，移除后台上游同步服务。
- 已回退 `/api/cost/upstream-sync/*`、`/api/cost/tools-access`、上游日志导入/匹配汇总、日志助手接入面板、充值倍率和最早同步时间等相关实现。
- 保留 `NewAPI 日志导出助手-1.2.2.user.js`，并升级到 1.2.13：导出文件名改为包含站点 host 和导出日期时间，例如 `newapi_logs_us.llmgate.io_20260504_155700.csv`；脚本已移除 Tools 上传、后台同步、检测接入等成本统计入口。
- 新增 `scripts/newapi_log_matcher.py`：读取 `日志/` 下多站点 CSV，按渠道 alias 映射上游，标准化模型名，并用 `输入Tokens + 输出Tokens + 总Tokens + 时间窗口` 做一对一匹配；输出 `report.html` 静态 UI、`summary.md`、`matches.csv`、`local_status.csv`、`unmatched_upstream.csv`、`by_upstream.csv`。
- 新增 `scripts/newapi_log_rules.example.json`：默认配置本地站 `newapi.youkies.space`，上游 `llmgate`、`omnai`、`opusclaw`、`guicore`，其中 `opusclaw` 按 1:10 充值比例使用 `cost_multiplier=0.1`。
- 新增 `docs/local-newapi-log-matcher.md`：记录本地使用方式、匹配口径和后续迁入 NewAPI Tools 的方向。
- 新增 Go 后端 `backend/internal/service/log_matcher.go` 与 `backend/internal/handler/log_matcher.go`：提供 `/api/log-match/analyze`，本站日志从数据库读取，上游 CSV 由 multipart 上传，支持文件名 alias 自动识别正式上游 host。
- 前端新增 `frontend/src/components/LogMatcher.tsx`，并在导航加入 `日志对账` 页；页面支持上传多个上游 CSV、设置时间窗口、查看总账/上游汇总，并按状态、上游、本站渠道、本站模型、按次和关键词筛选每条记录。
- 新增轻量上传链路：`backend/internal/service/log_match_upload_store.go` 将脚本上传的上游 CSV 保存到 `DATA_DIR/log_match_uploads`；`/api/log-match/uploads` 支持上传、列出、删除；`/api/log-match/analyze` 支持混合使用已上传文件 ID 和本次手动上传文件。
- userscript 升级到 1.2.14，恢复“导出后上传到 NewAPI Tools 日志对账”，只上传 CSV 和来源信息，不保存上游登录态。

## 验证结果

- `node --check "NewAPI 日志导出助手-1.2.2.user.js"` 通过。
- `go test ./...`（`backend/`）通过。
- `python -m py_compile backend-py/app/auth.py backend-py/app/cost_accounting_routes.py backend-py/app/cost_accounting_service.py` 通过。
- `npm run build`（`frontend/`）通过；仍有既有 CSS minify/chunk size warning 和大 chunk warning。
- `python -m py_compile scripts\newapi_log_matcher.py` 通过。
- `python scripts\newapi_log_matcher.py .\日志 --config scripts\newapi_log_rules.example.json` 通过；当前样本总账为本地收入 `$830.853798`、上游折算成本 `$703.264537`、毛利 `$127.589261`，匹配 3164 条、ambiguous 0 条、未匹配本地 1528 条。
- `report.html` 内联 JS 已通过 `node --check` 验证；UI 支持按匹配状态、上游站点、本站渠道、本站模型筛选，并可只看按次。
- NewAPI Tools 集成后，`go test ./...`（`backend/`）通过。
- NewAPI Tools 集成后，`npm run build`（`frontend/`）通过；仍有既有 CSS minify/chunk size warning 和大 chunk warning。
- 前端 dev server 已启动在 `http://127.0.0.1:3000/log-match`，Vite 代理 `/api` 到 `localhost:8000`。
- 轻量上传链路新增后，`node --check "NewAPI 日志导出助手-1.2.2.user.js"`、`go test ./...`（`backend/`）、`npm run build`（`frontend/`）通过；前端构建仍有既有 CSS/chunk warning。

## 注意

- 当前改动尚未提交或 push。
- 最近 5 个已推送提交（`ffa6174`、`80d62d4`、`7272ed0`、`be4bd1f`、`ff5a5bf`）未用 `git revert` 生成反向提交，而是在工作区按文件恢复到 `a749d55` 后保留为未提交改动。
- 当前本地工具默认只用渠道名做上游归属，避免 `claude-opus` 模型名误匹配到 `opusclaw`；模型名只参与匹配，不参与上游归属。
- 现有 CSV 无法保证每条都 100% 精确对应；工具会给每条本地日志标记 `matched`、`ambiguous` 或 `unmatched`，真正全量精确需要后续在转发链路保存 `local_request_id -> upstream_request_id` 映射。
- 本轮先完成 Go 后端和前端集成；Python 后端尚未新增 `/api/log-match/analyze` 兼容接口。
