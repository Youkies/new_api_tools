# 当前任务

## 目标

为自动分组“按消费升级”增加充值判定：用户必须有过成功充值行为，才允许按累计消费自动升级分组。

## 已完成

- Go 后端自动分组配置新增 `usage_require_topup`，默认开启。
- `by_usage` 候选用户查询新增成功充值 `EXISTS` 条件：`top_ups.user_id = users.id` 且状态为 `success`、`completed` 或 `1`。
- 如果要求充值但 `top_ups` 表不存在，按安全策略返回无候选用户。
- Python 兼容后端同步 `usage_require_topup` 配置和成功充值筛选。
- 前端自动分组配置页在“按消费升级”模式下新增“要求已充值/不要求充值”开关，并在预览表中显示充值判定列。

## 验证结果

- `go test ./...`（`backend/`）通过。
- `python -m py_compile backend-py\app\auto_group_service.py backend-py\app\auto_group_routes.py backend-py\app\local_storage.py backend-py\app\main.py` 通过。
- `npm run build`（`frontend/`）通过，仅保留既有 chunk 体积 warning。
- `git diff --check` 通过，仅有 CRLF 提示。

## 下一步

已按用户偏好准备自动提交并 push；等待用户线上试用充值判定后的消费升级候选结果。
