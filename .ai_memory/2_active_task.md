# 当前任务

## 目标

为 tools 的“日志分析”功能新增类似 NewAPI 日志导出助手的导出能力，但避免 userscript 逐页请求 NewAPI `/api/log` 在日志量大时产生大量请求并中断。

## 已完成

- Go 后端新增 `/api/analytics/export`：支持 CSV/JSON，按时间、类型、模型、用户名、令牌名、渠道、分组、Request ID、最大行数等条件直接查询 `logs` 表并流式写出。
- Python 后端补齐同名兼容接口和流式导出实现，保持 Go/Python API 路径一致。
- 前端 `日志分析` 页新增“导出”按钮和导出弹窗，默认导出当天消费日志，可选择 CSV/JSON、日志类型和常用筛选项。
- CSV 导出包含 BOM、中文表头、费用换算、tokens 汇总、倍率字段和合计行；JSON 导出追加格式化时间、USD 费用、总 tokens 和解析后的 `other` 字段。

## 验证结果

- `go test ./...`（`backend/`）通过。
- `python -m py_compile backend-py/app/log_analytics_service.py backend-py/app/log_analytics_routes.py` 通过。
- `npm run build`（`frontend/`）通过；仍有既有 CSS minify/chunk size warning。

## 下一步

按用户偏好准备检查 diff 后提交并 push 当前分支；上线后可用真实大日志库验证一次导出耗时和浏览器下载体验。
