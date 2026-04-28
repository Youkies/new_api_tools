# 当前任务

## 目标

针对用户反馈“用起来太卡”，完成一轮高收益性能优化：降低前端首屏包体、让 Go 后端具备真实系统规模检测和热缓存预热、减少模型状态 N+1 查询，并修复刷新缓存控制不生效的问题。

## 已完成

- 前端 `App.tsx` 改为路由级 `React.lazy` + `Suspense`，不再首屏一次性导入所有大页面。
- `main.tsx` 改为直接导入 `ToastProvider`，避免通过 `components/index.ts` 把所有页面拖进入口包。
- 模型状态页首次无配置时只默认选择前 20 个活跃模型，避免一次选中所有模型造成查询和渲染压力。
- Go 后端新增真实 `/api/system/scale` 和 `/api/system/warmup-status`，启动后后台预热排行榜、Dashboard、模型状态缓存。
- Go 后端新增周期性热缓存刷新，并按系统规模返回前端推荐刷新间隔。
- Go 模型状态查询改为按模型批量聚合，替代每个模型一次 SQL 的 N+1 查询。
- Go 风控排行榜、模型状态、IP 多 IP 查询补齐 `no_cache=true` 支持，手动刷新可以真正绕过旧缓存并刷新缓存。
- Go 查询补充超时控制，降低慢查询拖住请求的风险。
- 索引清单补充模型状态、成本核算、IP 聚合、按消费升级和充值判定相关索引。
- Go 正式后端新增自动分组后台定时扫描，尊重 `enabled`、`auto_scan_enabled` 和 `scan_interval_minutes` 配置。
- 修复 Dashboard 渠道状态查询不再硬编码 `channels.deleted_at`。

## 验证结果

- `go test ./...`（`backend/`）通过。
- `npm run build`（`frontend/`）通过。
- 构建后主入口包从约 `1.7 MB` 降至约 `33.87 KB`，大页面拆分为独立 chunk。

## 下一步

已准备提交并按用户偏好自动 push；线上还需要观察真实数据库下预热耗时、慢查询日志和索引创建情况。
