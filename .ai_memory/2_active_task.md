# 当前任务

## 目标

新增自动分组“按消费升级”模式：管理员可配置累计消费达到多少美元后自动升级到 Pro / Super / Ultra 等分组，并可试运行预览。

## 已完成

- Go 后端自动分组新增 `by_usage` 模式，配置项 `usage_rules` 保存消费门槛和目标分组。
- 扫描时按 `users.used_quota / 500000` 换算消费金额，命中最高门槛后升级到对应分组。
- 为避免误移动用户，仅从 `default` 或已配置档位中的低档分组升级；不会降级，也不会移动不在档位里的自定义分组。
- Python 后端补齐相同配置、预览和扫描逻辑，保持兼容。
- 前端自动分组页面新增“按消费升级”模式、默认 Pro/Super/Ultra 档位、档位增删改，以及预览表中的“已消费/目标分组”列。

## 验证结果

- `go test ./...`（`backend/`）通过。
- `python -m py_compile backend-py\app\auto_group_service.py backend-py\app\auto_group_routes.py backend-py\app\main.py` 通过。
- `npm run build`（`frontend/`）通过，仅保留既有 chunk 体积 warning。
- `git diff --check` 通过，仅有 CRLF 提示。

## 下一步

等待用户试用“按消费升级”模式；如需要，可继续增加每个档位当前人数统计、升级历史筛选或按月消费而非累计消费的规则。
