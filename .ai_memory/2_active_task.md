# 当前任务

## 目标

修复自动分组“按消费升级”规则里的目标分组下拉识别不全问题：需要识别 NewAPI 中已配置但当前没有用户的分组，例如 `Pro优`、`Ultra优`。

## 已完成

- 对照 `d:\Project\newapi` 源码确认 NewAPI 分组列表主要来自 `options.GroupRatio`，特殊分组还可能出现在 `UserUsableGroups`、`GroupGroupRatio`、`group_ratio_setting.group_special_usable_group`、`AutoGroups`、`abilities.group` 和 `channels.group`。
- Go 后端 `/api/auto-group/groups` 改为合并用户表、能力表、渠道表和 NewAPI options 配置里的分组，并保留用户数排序。
- Python 兼容后端同步相同分组来源解析，并对可选来源查询失败做跳过处理，避免整页分组列表被打空。
- 新增 Go 单元测试覆盖 `GroupRatio`、`UserUsableGroups`、`GroupGroupRatio`、特殊可用分组和 `AutoGroups` 的解析。

## 验证结果

- `go test ./...`（`backend/`）通过。
- `python -m py_compile backend-py\app\auto_group_service.py backend-py\app\auto_group_routes.py backend-py\app\main.py` 通过。
- `git diff --check` 通过，仅有 CRLF 提示。

## 下一步

等待用户在页面刷新后试用目标分组下拉；如仍有漏项，再用临时只读数据库连接核对实际 `options` 值，不在聊天或记忆里保存密码。
