# 当前任务

## 目标

为手动分组迁移增加“测试迁移”和“撤销操作”：管理员可以先预览所选用户从当前分组迁移到目标分组的结果，不落库；正式迁移后可按批次撤销。

## 已完成

- Go 后端 `/api/auto-group/batch-move` 新增 `dry_run`，测试迁移只返回可移动/跳过/失败明细，不更新 `users.group`。
- 正式手动迁移会生成 `manual:<unixnano>` 形式的 `batch_id`，写入自动分组日志；日志新增 `reverted_at`、`revert_log_id`、`revert_of` 元数据。
- 新增 Go `/api/auto-group/revert-batch`，可按 `batch_id` 撤销整批分组迁移；单条撤销也会标记原分配日志已撤销。
- Python 兼容后端同步 `dry_run`、`batch_id` 和批次撤销；本地 SQLite `auto_group_logs` 自动补列并增加批次索引。
- 用户管理页批量分组区域新增“测试迁移”“确认迁移”“撤销上次迁移”，测试结果会在页面中展示前 12 条明细。
- 自动分组日志页新增批次列，支持单条撤销和批次撤销，已撤销记录会禁用再次撤销。

## 验证结果

- `go test ./...`（`backend/`）通过。
- `python -m py_compile backend-py\app\auto_group_service.py backend-py\app\auto_group_routes.py backend-py\app\local_storage.py backend-py\app\main.py` 通过。
- `npm run build`（`frontend/`）通过，仅保留既有 chunk 体积 warning。
- `git diff --check` 通过，仅有 CRLF 提示。

## 下一步

等待用户试用手动迁移测试和批次撤销；如要发布，提交并 push 当前 7 个文件改动。
