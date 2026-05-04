# 本地 NewAPI 日志匹配核算器

这个工具用于先在本地把 NewAPI 多站点导出的 CSV 做匹配、核算和定价建议。它不连接数据库，不依赖 NewAPI Tools 后台，也不需要上游 token。

## 使用方式

1. 用 `NewAPI 日志导出助手` 分别导出本地站和上游站日志。
2. 把所有 CSV 放到同一个目录，例如 `日志/`。
3. 运行：

```powershell
python scripts\newapi_log_matcher.py .\日志 --config scripts\newapi_log_rules.example.json
```

工具会在 `日志/_match_reports/` 下生成一个时间戳目录，包含：

- `report.html`：本地静态 UI，可按匹配状态、上游站点、本站渠道、本站模型筛选查看每一条记录
- `summary.md`：总览、按上游盈亏、按次不亏价
- `by_upstream.csv`：每个上游的汇总表
- `matches.csv`：已匹配的本地请求和上游请求
- `local_status.csv`：每条本地请求的匹配状态
- `unmatched_upstream.csv`：没有被本地请求匹配到的上游记录

打开方式：用浏览器打开报告目录里的 `report.html` 即可，不需要启动后端服务。

## 匹配口径

当前匹配逻辑：

1. 用本地日志的 `渠道名称` 根据规则映射到上游站点。
2. 标准化模型名，例如 `「按次」claude-opus-4-6-渠道2` 会标准化为 `claude-opus-4-6`。
3. 精确匹配 `输入Tokens + 输出Tokens + 总Tokens`。
4. 时间差必须在配置的窗口内，默认 `120` 秒。
5. 如果候选唯一，标记为 `matched`；如果候选不唯一，标记为 `ambiguous`，不会强行匹配。

这属于高置信匹配。要做到数据库级别的 100% 精确，后续需要在 NewAPI Tools 或 NewAPI 转发链路中保存 `local_request_id -> upstream_request_id` 映射表。

当前 UI 会展示每条本地日志的状态：

- `matched`：已找到唯一上游候选
- `ambiguous`：有多个上游候选，工具未强行选择
- `unmatched`：未找到候选，常见原因是渠道未映射或上游 CSV 中缺少对应记录

## 配置

默认配置在 `scripts/newapi_log_rules.example.json`。

关键字段：

- `local_host`：本地站点 host，例如 `newapi.youkies.space`
- `time_window_seconds`：时间匹配窗口
- `upstreams.*.aliases`：通过本地 `渠道名称` 判断对应上游
- `upstreams.*.cost_multiplier`：上游成本倍率，例如 `opusclaw` 是 1:10 充值比例时填 `0.1`

可以复制一份到自己的日志目录：

```powershell
Copy-Item scripts\newapi_log_rules.example.json .\日志\newapi_log_rules.json
python scripts\newapi_log_matcher.py .\日志 --config .\日志\newapi_log_rules.json
```

## 当前迁移方向

Go 版 NewAPI Tools 已迁入基础日志对账能力：

- 本站日志从 Tools 连接的 NewAPI 数据库读取。
- 上游 CSV 可以在 `日志对账` 页手动上传，也可以由 userscript 导出后上传到 Tools 暂存区。
- `POST /api/log-match/uploads`：上传上游 CSV 到 `DATA_DIR/log_match_uploads`。
- `GET /api/log-match/uploads`：列出已上传 CSV，供前端勾选。
- `POST /api/log-match/analyze`：混合使用已上传文件和本次手动选择文件进行分析。

已经迁入的核心逻辑：

- CSV 解析和 host 识别
- 上游 alias 规则配置
- token/time/model 匹配器
- 匹配状态表：`matched`、`ambiguous`、`unmatched`
- 按上游、渠道、模型、按次价格的汇总
- 后续新增实时映射表，实现真正精确匹配

userscript 只上传导出的 CSV 和来源信息，不保存上游登录态，也不启动后台拉取任务。
