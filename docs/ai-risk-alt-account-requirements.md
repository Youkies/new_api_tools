# AI 风控小号识别需求文档

更新时间：2026-05-17  
适用范围：NewAPI Tools 风控中心、日志分析、用户管理联动能力  
当前策略：AI 辅助复核优先，默认不直接自动处罚

## 1. 背景

NewAPI Tools 已具备风险排行、IP 监控、AI 自动封禁配置、待复核、审计日志、白名单、共享 IP 批量封禁等基础能力。2026-05-17 已落地共享 IP 案件级 AI 研判兼容接口：

```http
POST /api/ai-ban/assess-shared-ip
```

该接口当前使用 `shared-ip-alt-account-v1` prompt，围绕“同 IP 多用户、未充值、低调用、首次出现集中、误报风险”生成 AI 复核建议。

同日已进一步落地实时“小号风险案件 v1”：规则层可生成共享 IP、30d 轮换账号池、邀请链、Token 轮换四类候选案件，前端已收敛为统一的“小号风险案件工作台”。共享 IP 不再作为第二套独立面板展示，而是小号证据链中的 `shared_ip` 案件类型，可在同一行内查看完整 IP、展开用户、AI 研判、封禁全部和撤销上一次。Go/Python 兼容后端提供通用案件列表、详情和 AI 研判接口：

```http
GET /api/risk/alt-account/cases
GET /api/risk/alt-account/cases/{case_id}
POST /api/risk/alt-account/cases/{case_id}/assess
```

当前 v1 仍是 live rules（实时规则生成）形态，尚未写入持久化案件表。下一阶段需要把实时案件升级成完整的“小号风险案件系统”，覆盖以下更隐蔽的场景：

- 同一 IP 或同一网段短时间注册/调用多个未充值账号。
- 一组账号每隔 24 小时轮换使用一个，规避 24h 窗口扫描。
- 邀请链、免费额度、同模型/同渠道/同时间习惯形成账号池。
- 多用户共享 IP 中混入校园网、公司 NAT、家庭宽带、运营商 CGNAT 或代理出口，需要降低误封。

## 2. 目标

### 2.1 产品目标

- 管理员进入风控中心时，可以看到“案件”而不是散落的 IP/用户排行榜。
- 对疑似小号账号池，系统能给出风险分、风险类型、证据摘要、疑似账号清单和建议动作。
- AI 的角色是证据整理和复核助手，不是不可控的自动处罚器。
- 所有封禁、注销、移动分组、观察名单动作必须可预览、可审计、可撤销或可补偿。

### 2.2 技术目标

- 以规则扫描生成候选案件，以 AI 进行重点案件复核，避免把全量日志发送给 AI。
- 支持 24h、3d、7d、30d 多窗口聚合，识别短时爆发和低频轮换两类小号。
- 尽量使用 `logs` 已有复合索引，避免大窗口全表扫描。
- Go 后端优先实现，Python 兼容后端保持接口语义一致。
- 不在文档、记忆库、数据库记录中保存数据库连接串、AI Key、用户 token 明文或其他 secret。

### 2.3 当前实现状态

截至 2026-05-17：

- 已实现 live rules 案件列表：`shared_ip`、`rotating_pool`、`invite_chain`、`token_rotation`。
- 已实现案件详情和通用 AI 研判接口，AI 请求发送聚合摘要、用户摘要和完整 IP，便于判断网段、出口和重复行为。
- 已实现前端 `AltAccountCasesPanel`，展示案件类型、风险分、未充值数、用户数、触发原因和 AI 研判结果。
- 已支持 30d 风控窗口，用于识别“每 24h 轮换一个账号”的接力型小号池。
- 已用真实数据库和真实 AI 网关验证：30d 可生成 127 个候选案件；24h 共享 IP 样本命中 15 个未充值账号，AI 输出 86/100、`review`、中等以上置信度。
- 尚未实现案件持久化表、AI 历史记录表、处置状态、反馈闭环和自动定时扫描。

## 3. 真实日志数据能力

基于当前真实数据库只读检查，`logs` 表可以支撑小号风控的行为聚类，但也有明确边界。

### 3.1 可用字段

`logs` 表可直接提供：

| 字段 | 用途 |
| --- | --- |
| `user_id` | 账号聚类、用户画像、跨窗口关联 |
| `created_at` | 时间窗口、首次出现、轮换节奏、突增判断 |
| `type` | 区分消费、充值、管理、系统、错误日志 |
| `username` | 管理员展示；按当前风控策略，通用案件 AI 可接收用户摘要，但不应发送邮箱、手机号、token 明文 |
| `token_name` / `token_id` | token 数量、token 轮换、同 token 多 IP |
| `model_name` | 模型习惯、同模型账号池 |
| `quota` | 消耗额度、免费额度消耗、成本异常 |
| `prompt_tokens` / `completion_tokens` | 调用规模和用量相似性 |
| `use_time` | 耗时异常、自动化调用特征 |
| `is_stream` | 流式调用习惯 |
| `channel_id` | 渠道偏好、同渠道账号池 |
| `group` | 分组偏好、是否绕过分组限制 |
| `ip` | 共享 IP、IP 轮换、网段聚类 |
| `request_id` | 下钻原始请求、错误聚合 |
| `other` | 请求路径、倍率、上游模型、错误码、状态码等扩展信息 |

### 3.2 数据覆盖情况

当前数据检查结论：

- `user_id`、`created_at`、`type`、`quota`、`prompt_tokens`、`completion_tokens`、`use_time`、`is_stream`、`channel_id`、`token_id` 覆盖完整。
- 消费日志中 `model_name`、`ip`、`request_id`、`other` 覆盖率较高，适合做风控聚合。
- 错误日志中 `ip`、`model_name`、`request_id`、`other` 覆盖率也较高，适合识别同类错误和自动化调用。
- `channel_name` 在 logs 中不可用，应通过 `channels` 表按 `channel_id` 关联。
- 消费日志 `content` 覆盖极低，不能依赖 prompt 内容相似性作为主要证据。

### 3.3 需要关联的表

为了减少误判，应在候选案件生成时关联：

| 表 | 用途 |
| --- | --- |
| `users` | 状态、角色、分组、邀请人、注册时间、累计额度 |
| `top_ups` | 是否成功充值、充值次数、最近充值时间 |
| `tokens` | 用户 token 数量、token 创建时间、token 状态 |
| `channels` | 渠道名称、渠道类型、模型映射 |
| `abilities` | 分组与模型可用性 |
| `options` | 分组倍率、可用分组、自动分组配置 |

### 3.4 当前缺失但高价值的数据

以下数据如果后续 NewAPI 或 Tools 能采集，将显著提升小号识别准确率：

- 注册 IP、登录 IP、最后登录 IP。
- User-Agent、客户端类型、SDK 标识。
- 设备指纹、浏览器语言、时区。
- IP ASN、国家/省市、运营商、代理/VPN/机房标签。
- OAuth 绑定来源、邮箱域名、注册来源。
- 请求内容的安全摘要，例如 prompt hash 或模板指纹，而不是原文。

## 4. 风险场景

### 4.1 同 IP 批量小号

特征：

- 同一 IP 在短窗口出现多个不同用户。
- 多数账号未充值。
- 多数账号低调用、低额度、刚开始使用。
- token 数接近用户数，例如 15 用户 15 token。
- 首次出现时间高度集中。

建议风险标签：

```text
MULTI_USER_SHARED_IP
FREE_QUOTA_FARMING
SHORT_LIFECYCLE_ACCOUNT
```

默认处置：

- 高风险：进入待复核，展示批量封禁预览。
- 中风险：加入观察名单，后续窗口继续追踪。
- 低风险：仅记录案件，避免误伤共享网络。

### 4.2 24h 轮换账号池

用户提出的典型规避方式：有人开 20 个账号，但是每隔 24h 只用一个。

该场景不能只靠 24h 共享 IP 判断，需要 30d 轮换账号池检测。

特征：

- 30d 内存在 10-20 个账号依次活跃。
- 每个账号只有 1 个或少数活跃日。
- 账号之间 IP、网段、ASN、邀请人、模型、渠道、时间段习惯相似。
- 一个账号额度耗尽或接近耗尽后，下一个账号开始出现。
- 多数账号无成功充值，或充值金额/次数异常一致。
- 账号之间同时在线重叠少，表现为“接力”而不是“群体爆发”。

建议风险标签：

```text
ROTATING_ALT_ACCOUNT_POOL
FREE_QUOTA_FARMING
MODEL_USAGE_SIMILARITY
SUBNET_ASN_ACCOUNT_CLUSTER
```

默认处置：

- 首次发现：生成案件并进入 AI 复核。
- AI 高置信但仍有共享网络可能：建议限制免费额度、移动到观察分组或人工复核。
- 与邀请链、充值异常叠加时：允许管理员批量封禁预览。

### 4.3 邀请链小号

特征：

- 多个新账号来自同一邀请人或同一邀请链。
- 被邀请账号无充值或低充值，但快速消耗免费额度。
- 被邀请账号使用同一 IP/网段/模型/渠道。
- 邀请收益与账号消耗行为存在明显相关。

建议风险标签：

```text
INVITE_CHAIN_ALT_ACCOUNTS
FREE_QUOTA_FARMING
```

默认处置：

- 先复核邀请链，不直接处罚上游邀请人。
- 可对被邀请账号做观察、分组限制或批量封禁预览。

### 4.4 Token 与 IP 轮换

特征：

- 同一用户短时间创建多个 token 并轮换调用。
- 同一 token 跨多个 IP 使用。
- 多个账号的 token 命名、创建时间、调用模型高度相似。
- token 调用失败模式一致，例如相同错误码、相同请求路径。

建议风险标签：

```text
TOKEN_ROTATION_ABUSE
CLIENT_FINGERPRINT_CLUSTER
```

默认处置：

- 优先冻结 token 或移动观察分组，而不是直接封禁账号。
- 对同 token 多 IP 且高失败率的情况，提示可能是泄露 token。

### 4.5 模型/渠道/时间习惯相似

特征：

- 多个账号只调用同一组模型或同一渠道。
- prompt/completion token 分布接近。
- 调用时间段固定，例如每天同一小时。
- 错误码、请求路径、上游模型映射一致。

建议风险标签：

```text
MODEL_USAGE_SIMILARITY
TIME_PATTERN_SIMILARITY
ERROR_PATTERN_CLUSTER
```

默认处置：

- 作为辅助证据参与评分，不单独触发封禁。
- 与 IP、未充值、邀请链叠加后提升风险等级。

## 5. 案件生成规则

### 5.1 规则扫描与 AI 复核分层

系统应分两层工作：

1. 规则扫描层
   - 低成本 SQL 聚合。
   - 每 30-60 分钟运行。
   - 生成候选案件、更新案件分数。
   - 不调用 AI。

2. AI 复核层
   - 每 5 小时处理高风险案件。
   - 每晚运行 7d/30d 深度案件复核。
   - 只发送聚合摘要和最多 40 个用户摘要。
   - 输出证据、误报风险、复核问题和建议动作。

### 5.2 共享 IP 案件规则

候选条件：

- 时间窗口：默认 24h，可选 3d、7d。
- `distinct user_id >= 3`。
- `unbanned_count >= 2`。
- 未充值用户占比 `>= 60%`。
- 低调用用户占比 `>= 50%`，低调用定义可配置，例如 `total_request_count <= 3` 或 `used_quota <= 0.2 USD`。

风险加分：

- 用户数越多，加分越高。
- token 数接近用户数，加分。
- 首次出现跨度越短，加分。
- 已封禁用户混入，加分。
- 成功充值用户越多，降低风险。
- 管理员、白名单、受保护分组降低或直接排除。

### 5.3 轮换账号池案件规则

候选条件：

- 时间窗口：默认 30d，辅助窗口 7d。
- 账号池候选用户数 `>= 8`。
- 每个账号活跃日数量中位数 `<= 2`。
- 账号间共享以下任意两类特征：
  - IP 或 /24 网段相同。
  - 邀请人相同。
  - 常用模型集合相似。
  - 常用渠道集合相似。
  - 调用小时分布相似。
  - 未充值且使用免费额度。

风险加分：

- 活跃账号呈接力顺序，加分。
- 前一个账号额度耗尽后下一个账号出现，加分。
- 20 个以内账号构成稳定池，加分。
- 有共享 IP 但 24h 不重叠，加分。
- 全部无充值或同额小额充值，加分。

### 5.4 邀请链案件规则

候选条件：

- 同一邀请人 30d 内邀请账号数 `>= 5`。
- 被邀请账号未充值占比 `>= 70%`。
- 被邀请账号中有调用记录占比 `>= 30%`。

风险加分：

- 被邀请账号共享 IP/网段。
- 被邀请账号模型/渠道习惯一致。
- 被邀请账号短生命周期。
- 邀请人自身也存在异常消耗或封禁历史。

### 5.5 Token 异常案件规则

候选条件：

- 同一 token 在 24h 内跨 `>= 3` 个 IP。
- 同一用户 24h 内 token 数增长异常。
- 多个账号 token 创建时间或命名模式高度相似。

风险加分：

- 高失败率。
- 同一请求路径或错误码反复出现。
- 同一 token 跨地区或跨 ASN。

## 6. 定时策略

### 6.1 推荐频率

| 任务 | 频率 | 时间窗口 | 是否调用 AI | 目标 |
| --- | --- | --- | --- | --- |
| 轻量规则扫描 | 每 30-60 分钟 | 1h / 24h | 否 | 快速发现共享 IP、突增、异常 token |
| 共享 IP AI 复核 | 每 5 小时 | 24h + 3d 摘要 | 是 | 判断短时小号、共享网络误报 |
| 夜间深度扫描 | 每天 1 次 | 7d / 30d | 是 | 识别 24h 轮换账号池、邀请链 |
| 案件复算 | 管理员打开详情时 | 当前案件窗口 | 可选 | 刷新证据，避免旧结论误导 |

### 6.2 为什么不是只发 24h

24h 足够识别短时间批量注册、同 IP 多号、免费额度集中使用，但无法识别“每天只用一个号”的轮换账号池。因此必须保留：

- 24h：用于爆发和当前风险。
- 3d：用于确认是否连续出现。
- 7d：用于观察小规模轮换。
- 30d：用于识别 10-20 个账号的低频接力。

### 6.3 AI 调用预算控制

AI 不应处理所有候选，只处理：

- 风险分超过阈值的案件。
- 新出现且未复核过的案件。
- 距离上次复核超过冷却时间且证据发生明显变化的案件。
- 管理员手动点击“AI 研判”的案件。

建议默认限制：

- 每次 5 小时任务最多处理 20 个共享 IP 案件。
- 每晚深度任务最多处理 20 个轮换账号池案件、20 个邀请链案件。
- 单个案件发送用户摘要最多 40 个。
- 超过 40 个用户时，发送 Top 风险用户 + 分布统计。

### 6.4 去重与冷却

案件指纹建议：

```text
case_fingerprint = hash(case_type + primary_key + window + top_user_ids + evidence_version)
```

同一指纹在冷却期内不重复调用 AI。若新增用户数、未充值比例、封禁状态、调用量变化超过阈值，则生成新版本。

## 7. AI 输入数据要求

### 7.1 总原则

发送给 AI 的内容应是案件级聚合证据，不是原始日志。

允许发送：

- 完整 IP、网段和 IP 指纹，用于判断共享出口、账号池轮换和重复出现。
- 用户 ID。
- 用户状态、角色、分组。
- 是否成功充值、充值次数。
- 请求数、token 数、额度消耗。
- 首次出现和最后出现时间。
- 模型/渠道聚合分布。
- 错误码、请求路径、状态码的聚合统计。
- 风险标签和规则命中原因。

禁止发送：

- 数据库连接串、API Key、Bearer token。
- token 明文、上传专用 Key。
- 邮箱、手机号、OAuth 标识等可识别个人信息，除非仅在本地模型处理。
- prompt/response 原文。
- 支付订单号、支付凭证、充值回调内容。

### 7.2 共享 IP AI 请求结构

```json
{
  "case_type": "MULTI_USER_SHARED_IP",
  "prompt_version": "shared-ip-alt-account-v1",
  "window": "24h",
  "case": {
    "ip": "223.160.168.98",
    "is_whitelisted_ip": false,
    "known_network_type": "unknown"
  },
  "case_stats": {
    "user_count": 15,
    "token_count": 15,
    "request_count": 18,
    "unbanned_count": 15,
    "banned_count": 0,
    "no_topup_count": 15,
    "low_request_user_count": 15,
    "first_seen_spread_seconds": 4171
  },
  "users": [
    {
      "user_id": 123,
      "status": 1,
      "role": "user",
      "group": "default",
      "request_count": 1,
      "total_request_count": 1,
      "token_count": 1,
      "used_quota": 0,
      "topup_count": 0,
      "first_seen": 1710000000,
      "last_seen": 1710000500
    }
  ]
}
```

### 7.3 轮换账号池 AI 请求结构

```json
{
  "case_type": "ROTATING_ALT_ACCOUNT_POOL",
  "prompt_version": "rotating-alt-account-pool-v1",
  "window": "30d",
  "case_stats": {
    "candidate_user_count": 20,
    "active_days_median": 1,
    "no_topup_count": 19,
    "shared_ip_count": 6,
    "shared_subnet_count": 3,
    "shared_inviter_count": 1,
    "model_similarity_score": 0.82,
    "channel_similarity_score": 0.76,
    "sequential_activation_score": 0.88
  },
  "timeline": [
    {
      "date": "2026-05-01",
      "active_user_ids": [101],
      "request_count": 12,
      "quota": 12345
    }
  ],
  "users": [
    {
      "user_id": 101,
      "active_days": 1,
      "first_seen": 1710000000,
      "last_seen": 1710003600,
      "request_count": 12,
      "used_quota": 12345,
      "topup_count": 0,
      "main_models": ["gpt-5.5"],
      "main_channel_ids": [7],
      "ip_fingerprints": ["iphash:abc"]
    }
  ]
}
```

### 7.4 AI 输出结构

AI 输出必须归一化为稳定 schema：

```json
{
  "risk_score": 88,
  "confidence": 0.82,
  "action": "review",
  "risk_labels": [
    "ROTATING_ALT_ACCOUNT_POOL",
    "FREE_QUOTA_FARMING"
  ],
  "evidence_summary": [
    "30d 内 20 个账号呈接力活跃，每个账号活跃日中位数为 1",
    "19 个账号无成功充值记录",
    "模型和渠道偏好高度相似"
  ],
  "false_positive_risk": "medium",
  "false_positive_reasons": [
    "共享 IP 可能来自校园网或公司 NAT",
    "缺少设备指纹和注册 IP 证据"
  ],
  "questions_for_admin": [
    "这些账号是否来自同一邀请链？",
    "该 IP 或网段是否为可信网络？"
  ],
  "likely_user_ids": [101, 102, 103],
  "recommended_actions": [
    "进入待复核",
    "限制免费额度或移动观察分组",
    "复核邀请链和充值记录"
  ],
  "prompt_version": "rotating-alt-account-pool-v1"
}
```

归一化要求：

- `risk_score` 限制在 0-100。
- `confidence` 限制在 0-1。
- `action` 只能是 `monitor`、`review`、`ban`。
- 在 `pending_review_first` 策略下，除非显式开启高置信自动执行，否则 `ban` 也应进入待复核。
- `false_positive_risk` 只能是 `low`、`medium`、`high`。
- 数组字段缺失时返回空数组，不让前端崩溃。

## 8. 后端需求

### 8.1 案件列表

```http
GET /api/risk/alt-account/cases
```

实现状态：v1 已落地。当前由 live rules 实时生成，不读取持久化案件表。

查询参数：

| 参数 | 说明 |
| --- | --- |
| `case_type` | `shared_ip` / `rotating_pool` / `invite_chain` / `token_rotation` |
| `status` | `open` / `reviewed` / `ignored` / `resolved` |
| `risk_level` | `low` / `medium` / `high` / `critical` |
| `window` | `24h` / `3d` / `7d` / `30d` |
| `limit` / `offset` | 分页 |

响应要点：

- 案件 ID、案件类型、风险分、风险等级。
- 核心对象，例如 IP 指纹、邀请人 ID、账号池 ID。
- 用户数、未充值数、请求数、token 数、首次/最后出现时间。
- 最近 AI 研判摘要。
- 当前处置状态。

### 8.2 案件详情

```http
GET /api/risk/alt-account/cases/{case_id}
```

实现状态：v1 已落地。当前根据 `case_id` 前缀只重建对应案件类型，避免为详情和 AI 研判重新生成全部类型。

响应要点：

- 完整案件摘要。
- 命中规则。
- 用户明细。
- 时间线。
- IP/网段/模型/渠道分布。
- 充值和邀请链摘要。
- AI 研判历史。
- 管理员处置记录。

### 8.3 手动触发扫描

```http
POST /api/risk/alt-account/scan
```

请求：

```json
{
  "case_types": ["shared_ip", "rotating_pool"],
  "windows": ["24h", "30d"],
  "dry_run": true,
  "max_cases": 50
}
```

要求：

- `dry_run=true` 只返回预览，不写案件。
- 正式执行写入扫描批次。
- 返回扫描窗口、命中数量、跳过原因和耗时。

### 8.4 AI 研判

复用现有接口：

```http
POST /api/ai-ban/assess-shared-ip
```

新增通用接口：

```http
POST /api/risk/alt-account/cases/{case_id}/assess
```

实现状态：v1 已落地。当前支持请求级 override `base_url`、`api_key`、`model`，override 仅用于本次调用，不持久化。

要求：

- 默认使用后台保存的 AI 配置。
- 支持一次性 override，但不得持久化 override 中的 base_url、api_key。
- 模型名必须使用 AI 网关 `/v1/models` 返回的真实模型 ID；带中文前缀的模型 ID 需要以 UTF-8 JSON 发送。
- 调用前记录 prompt 版本和输入摘要 hash。
- 调用后保存归一化结果、原始模型名、token 用量、耗时、错误信息。

### 8.5 案件处置

```http
POST /api/risk/alt-account/cases/{case_id}/resolve
```

请求：

```json
{
  "action": "mark_reviewed",
  "note": "人工确认共享网络，暂不处理",
  "user_action": {
    "type": "move_group",
    "target_group": "watch",
    "user_ids": [101, 102]
  },
  "dry_run": true
}
```

支持动作：

- `mark_reviewed`：标记已复核。
- `ignore`：忽略案件，可设置忽略到期时间。
- `watch`：加入观察名单。
- `move_group`：移动到观察或限制分组。
- `ban_users`：批量封禁用户。
- `whitelist_network`：将 IP/网段加入白名单或低权重列表。
- `false_positive`：标记误报，用于后续规则调参。

所有动作必须支持 `dry_run`。

### 8.6 配置接口

```http
GET /api/risk/alt-account/config
PUT /api/risk/alt-account/config
```

配置项：

- 扫描开关。
- 轻量扫描间隔。
- AI 复核间隔，默认 5 小时。
- 夜间深度扫描时间。
- 每批最大 AI 案件数。
- 共享 IP 阈值。
- 轮换账号池阈值。
- 邀请链阈值。
- 受保护角色/分组。
- IP 白名单、网段白名单。
- 是否允许极高风险自动执行，默认关闭。

## 9. 数据库需求

建议新增工具侧表，不修改 NewAPI 核心业务表。

### 9.1 案件表

```sql
api_tools_alt_account_cases(
  id,
  case_type,
  case_key,
  case_fingerprint,
  status,
  risk_score,
  risk_level,
  window,
  primary_ip,
  primary_ip_hash,
  primary_user_id,
  primary_inviter_id,
  user_count,
  request_count,
  token_count,
  no_topup_count,
  first_seen,
  last_seen,
  evidence_json,
  rule_hits_json,
  latest_ai_assessment_id,
  created_at,
  updated_at,
  resolved_at
)
```

### 9.2 案件用户表

```sql
api_tools_alt_account_case_users(
  id,
  case_id,
  user_id,
  status,
  role,
  group_name,
  request_count,
  total_request_count,
  token_count,
  used_quota,
  topup_count,
  active_days,
  first_seen,
  last_seen,
  evidence_json,
  created_at
)
```

### 9.3 AI 研判记录表

```sql
api_tools_alt_account_ai_runs(
  id,
  case_id,
  prompt_version,
  input_hash,
  model,
  risk_score,
  confidence,
  action,
  risk_labels_json,
  evidence_summary_json,
  false_positive_risk,
  false_positive_reasons_json,
  questions_for_admin_json,
  likely_user_ids_json,
  recommended_actions_json,
  usage_json,
  raw_output_json,
  error_message,
  created_at
)
```

### 9.4 扫描批次表

```sql
api_tools_alt_account_scan_runs(
  id,
  scan_type,
  windows_json,
  dry_run,
  status,
  candidate_count,
  case_count,
  skipped_count,
  started_at,
  finished_at,
  error_message
)
```

## 10. 前端需求

### 10.1 风控中心结构

风控中心建议拆为：

```text
frontend/src/components/risk/
  RiskCenter.tsx
  RiskQueue.tsx
  IPCaseList.tsx
  AltAccountCaseList.tsx
  AltAccountCaseDetail.tsx
  AltAccountCasesPanel.tsx
  AltAccountAIResultCard.tsx
  RiskActionHistory.tsx
  RiskPolicySettings.tsx
  BulkActionDialog.tsx
  hooks/
    useRiskQueue.ts
    useAltAccountCases.ts
    useRiskActions.ts
```

已存在：

- `AltAccountCasesPanel.tsx`
- `risk/types.ts`

下一步重点：

- 从 `RealtimeRanking.tsx` 继续拆出 AI 结果卡片和风险 action hooks。
- 将 `AltAccountCasesPanel.tsx` 进一步拆成 `AltAccountCaseList`、`AltAccountCaseDetail` 和 `AltAccountAIResultCard`。
- 保持共享 IP 作为 `shared_ip` 案件类型，不再恢复第二套共享 IP 小号面板。

### 10.2 案件列表

列表字段：

- 风险等级、风险分。
- 案件类型。
- 用户数、未充值数、请求数、token 数。
- 首次出现、最后出现。
- 最近 AI 建议。
- 当前状态。
- 操作：查看详情、AI 研判、忽略、加入观察、批量处置。

筛选：

- 案件类型。
- 风险等级。
- 状态。
- 时间窗口。
- 是否已 AI 研判。
- 是否含已封禁用户。
- 是否全部未充值。

### 10.3 案件详情

详情页必须展示：

- 案件概览。
- 规则命中原因。
- AI 研判结果。
- 误报风险。
- 复核问题。
- 用户清单。
- 时间线。
- IP/网段/模型/渠道分布。
- 充值/邀请摘要。
- 操作审计。

### 10.4 AI 结果卡片

卡片字段：

- 风险分、置信度、建议动作。
- 风险标签。
- 证据摘要。
- 误报风险和误报原因。
- 管理员复核问题。
- 疑似账号列表。
- prompt 版本、模型、token 用量、研判时间。

交互：

- 一键进入待复核。
- 标记误报。
- 忽略本案件。
- 创建批量封禁 dry-run。
- 移动到观察分组 dry-run。

### 10.5 误操作保护

- 所有危险按钮需要二次确认。
- 批量操作必须先展示影响用户数和样本。
- 管理员、白名单、受保护分组默认排除。
- 共享网络高误报时，按钮文案应偏向“复核/观察”，不默认“封禁”。

## 11. 日志分析联动

从案件详情应能跳转到日志分析：

- 按用户 ID + 时间窗口查看原始日志。
- 按 IP + 时间窗口查看共享 IP 记录。
- 按模型/渠道查看相似调用。
- 按错误码/请求路径查看失败聚类。

日志分析页应支持接收以下查询参数：

```text
user_id
ip
ip_hash
model_name
channel_id
type
start_timestamp
end_timestamp
request_id
risk_case_id
```

管理员风控台默认展示完整 IP；共享 IP 是小号案件证据链的一部分，不在前端做脱敏。若未来增加低权限只读角色，可再按角色权限隐藏原始 IP，并通过 `risk_case_id` 在服务端还原筛选条件。

## 12. 安全与隐私

- 数据库连接串、AI Key、上传专用 Key 永远不写入文档、记忆库、前端日志或审计明文。
- 按当前管理员风控口径，AI 小号案件研判可以发送完整 IP、网段和案件用户摘要。
- 邮箱、手机号、OAuth 标识、用户 token、API Key、数据库连接串不得发送给外部 AI。
- prompt/response 原文不发送给 AI；如需内容相似性，只发送 hash 或模板指纹。
- AI 原始输出可保存，但不得包含 secret；保存前应截断异常长内容。
- 所有管理员动作写审计日志。

## 13. 验收标准

### 13.1 共享 IP 小号

- 系统能列出 24h 内同 IP 多用户案件。
- 每个案件展示完整 IP、用户数、未充值数、token 数、请求数、首次出现跨度。
- 管理员可展开案件用户列表，查看账号状态、充值线索、请求数、token 数、首次/最近出现时间。
- 管理员可对案件触发 AI 研判。
- 管理员可在同一个工作台执行单用户分析/封禁、共享 IP 案件封禁全部和撤销上一次批量封禁。
- AI 输出包含风险分、建议动作、证据、误报风险、复核问题。
- 危险动作默认进入 dry-run 预览。

2026-05-17 真实库验收记录：

- 24h 共享 IP 案件可正常生成，最高风险样本为 15 用户、15 未充值、15 token、18 请求。
- 通用案件 AI 研判使用 `「按量」gpt-5.5` 验证通过，模型返回风险分 86、动作 `review`，符合 `pending_review_first` 策略。
- 本次验证暴露 PowerShell 非 UTF-8 请求体会导致中文模型 ID 被传成问号；浏览器和 Go 后端 JSON 请求应保持 UTF-8。

### 13.2 24h 轮换账号池

- 系统能在 30d 窗口识别“账号接力使用”候选案件。
- 即使单个 24h 内不出现多账号共享 IP，也能通过 30d 时间线聚合发现。
- 案件详情能展示每日活跃账号时间线。
- AI 能明确回答“为什么像轮换小号”和“可能误报的原因”。

### 13.3 邀请链小号

- 系统能识别同一邀请人下的未充值消耗账号群。
- 案件详情能展示邀请链摘要和被邀请账号行为。
- 不直接处罚邀请人，必须提供人工复核入口。

### 13.4 可审计与可回滚

- 每次 AI 研判可追踪 prompt 版本、模型、输入摘要 hash、输出结果。
- 每次批量动作有 `batch_id`。
- 批量封禁/分组迁移支持撤销或补偿。
- 误报标记能进入后续规则调参统计。

### 13.5 性能

- 轻量扫描不应长时间锁表。
- 30d 深度扫描应使用聚合和限流，避免频繁扫全量日志。
- 前端案件列表默认分页。
- AI 调用有批量上限、冷却和失败重试限制。

## 14. 分期计划

### 阶段一：案件文档和 UI 拆分

- 已完成本需求文档。
- 已在 `功能优化方案.md` 增加需求文档入口。
- 已新增 `AltAccountCasesPanel`，但 `RealtimeRanking.tsx` 仍需继续拆：
  - `AltAccountAIResultCard`
  - `IPCaseList`
  - risk hooks

### 阶段二：实时案件 v1

- 已将共享 IP、30d 轮换账号池、邀请链、Token 轮换纳入 `/api/risk/alt-account/cases`。
- 已新增通用 AI 研判接口 `/api/risk/alt-account/cases/{case_id}/assess`。
- 已完成前端案件列表面板。

### 阶段三：案件持久化和处置闭环

- 新增案件表、案件用户表、AI 研判记录表。
- 新增案件状态、忽略、复核、处置记录和管理员反馈。
- 将实时规则扫描改为后台定时生成/更新案件。

### 阶段四：轮换账号池深化

- 已新增 30d 轮换账号池 live rules 和 `rotating-alt-account-pool-v1` prompt。
- 继续增强 7d/30d 账号接力特征、额度耗尽接续、模型/渠道相似性。
- 新增时间线可视化。

### 阶段五：邀请链与 token 异常深化

- 已新增邀请链和 token 轮换 live rules。
- 继续补充邀请收益、注册来源、token 创建时间和 token 跨 IP 维度。
- 与用户详情、日志分析联动。

### 阶段六：反馈闭环

- 统计 AI 命中率、管理员确认率、误报率。
- 按误报反馈调整规则权重。
- 支持网络白名单、分组保护、策略版本管理。

## 15. 待确认问题

- 观察分组名称是否固定，例如 `watch`，还是由管理员配置。
- 免费额度消耗的准确门槛是多少，按 quota 还是按 USD。
- 是否能从 NewAPI 侧补充注册 IP、登录 IP、User-Agent。
- 是否需要接入 IP ASN/geo/provider 数据源。
- 是否允许本地部署模型处理更敏感的用户标识。
- 高置信自动执行是否保持默认关闭。
