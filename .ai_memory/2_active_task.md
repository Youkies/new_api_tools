# 当前任务

## 目标

新增成本核算功能：查询每个渠道在自定义时间段内的站内计费、上游成本、毛利估算和模型明细；默认时间范围为本地今天 00:00 到页面打开时刻；支持设置渠道模型基础成本和渠道倍率，并可把多个站内模型名映射到同一个上游模型。

## 已完成

- Go 后端新增 `/api/cost/summary`、`/api/cost/rules`，使用工具自建表 `api_tools_channel_costs` 保存成本规则。
- 成本规则支持按量（输入/输出 `$ / 1M tokens`）和按次（`$ / request`），支持渠道默认规则 `*`、具体模型规则和 `cost_multiplier` 倍率；实际成本按基础价格乘倍率计算。
- 成本查询按 `logs.channel_id + logs.model_name` 聚合请求数、额度消耗、输入/输出 tokens，并计算站内计费、上游成本、毛利和未配置模型数量。
- 修正上游模型来源：成本汇总会读取 `channels.model_mapping`，把 NewAPI 渠道里的模型重定向作为默认上游模型；成本规则仍可显式覆盖，且规则可按站内别名或上游模型匹配。
- Python 后端补齐相同 `/api/cost/*` 兼容接口。
- 前端新增 `成本核算` 导航页，可按时间段和渠道查询，可从未配置模型行快速生成成本规则并保存。
- 前端成本规则表新增“倍率”列，基础价格列用于填写官方/正常价格，例如输入 `5`、倍率 `0.35` 会按 `1.75 $/1M` 计入成本。

## 验证结果

- `go test ./...`（`backend/`）通过。
- `python -m py_compile backend-py\app\cost_accounting_service.py backend-py\app\cost_accounting_routes.py backend-py\app\main.py` 通过。
- `npm run build`（`frontend/`）通过，仅保留既有 chunk 体积 warning。

## 下一步

等待用户确认成本核算页里的“上游模型”是否已与渠道管理中的模型重定向一致；如需要，可继续增加导出 CSV、规则批量复制或按上游模型汇总视图。
