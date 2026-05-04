# 当前任务

## 目标

完善 NewAPI Tools 日志对账的脚本上传体验：上游 NewAPI 站点用 userscript 导出 CSV 后，可勾选上传到 Tools；Tools 前端需要直接展示脚本要填写的地址和凭证，减少配置成本。

## 已完成

- 已将日志对账迁入 Go 版 NewAPI Tools：本站日志从数据库读取，上游 CSV 手动上传或脚本上传后分析。
- 已新增 `/api/log-match/uploads` 暂存链路，userscript 1.2.14 支持导出后上传 CSV 到 Tools 日志对账。
- 本轮将脚本上传鉴权改为“日志上传专用 Key”：
  - 新增 `GET/POST /api/log-match/upload-key`，可在前端生成/修改/复制上传专用 Key。
  - 上传专用 Key 保存到 `DATA_DIR/log_match_upload_key.json`，Zeabur 挂载目录按 `/app/data` 使用时设置 `DATA_DIR=/app/data`。
  - `AuthMiddleware` 只在 `POST /api/log-match/uploads` 接受该专用 Key，不开放其他接口权限。
  - `frontend/src/components/LogMatcher.tsx` 的“脚本上传接入”区显示 Tools 地址、上传接口和上传专用 Key 管理 UI。
  - userscript 升级到 1.2.15，文案改为推荐填写上传专用 Key。

## 验证结果

- `node --check "NewAPI 日志导出助手-1.2.2.user.js"` 通过。
- `go test ./...`（`backend/`）通过。
- `npm run build`（`frontend/`）通过；仍有既有 CSS minify warning 和大 chunk warning。

## 注意

- 当前本轮前端展示改动尚未提交或 push。
- 现有 CSV 匹配仍不能保证每条 100% 精确对应；全量精确需要后续在转发链路保存 `local_request_id -> upstream_request_id` 映射。
- Python 后端尚未新增 `/api/log-match/analyze` 兼容接口。
