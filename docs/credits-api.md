# Credits 计费与 Desktop API 契约

本版本由 Transit Hub Server、管理 Website 实现。Desktop 代码不在本次改动范围内。

## 单位与范围

- 固定 `1 CNY = 100 Credits`，`1 Credit = 10,000 micro-CNY`。
- API/SQLite 继续使用整数微元字段，前端用 `micro / 10_000` 显示 Credits。消费允许四位小数 Credits，不按请求取整到一分钱。
- 生命周期预算按 API Key 独立管理，多个 Key 不自动合并成用户预算。
- 配置 `CURRENCY=CNY`；已有其他币种价格必须先人工转换，系统不根据汇率重估历史消费。
- 金额计算各项求和后四舍五入到微元，整数乘法使用大整数避免中间溢出，计数溢出饱和为 int64 最大值。
- 此功能不是充值钱包；修改总额度是修改生命周期消费上限，不清空已消费。

## 软额度与窗口

请求开始检查：总金额、窗口金额、既有请求数/Token 限额均未耗尽才放行。完成后短锁更新内存计数，后台异步落盘，不预占、不冻结、不跨请求持有事务。已放行请求不会因余额耗尽被中断，因此消费和剩余可超过额度、低于零。

`cost_quota_micro=0` 表示不限。负配额拒绝。PATCH 省略字段不修改，传 `0` 取消限制。

窗口继续使用 `RATE_LIMIT_TIMEZONE`（默认 Asia/Shanghai）：1h 为自然小时，1d 为自然日，7d 为自然周；5h/30d 沿用固定持续时间窗口，不是滑动窗口。跨窗口请求按开始时间归属；日志 `created_at` 仍是完成时间，报表按完成时间统计，与限流窗口的时间口径不同。

`MAX_CONCURRENT_PER_KEY` 默认 16，必须为正整数；仅限制同时在途请求，不预占金额。计费不补充、修改或限制客户端输出额度；未指定时由上游处理，指定时原样透传。旧价格 JSON 中的 `default_max_output_tokens` 和 `max_output_tokens` 已废弃并忽略，无需迁移数据库。图片 `n` 为 1–10。超额没有固定金额保证，取决于单次费用与并发数。

用量库异常时维持单实例内存计数并在恢复后补写；异常退出可能丢失尚未落盘用量。禁止多个独立实例共同执行同一 Key 的预算。日志为尽力记录，可丢弃、定期清理，不能用日志 SUM 重建权威余额。

## Key 配置与响应

以下管理员写接口增加 `cost_quota_micro`：

- POST `/admin/api-keys`
- PATCH `/admin/api-keys/{id}`
- POST `/admin/jwt-grants`
- PATCH `/admin/jwt-grants/{jti}`

示例：总额度 10,000 Credits（100 元），每小时 500 Credits（5 元）。

```json
{
  "name": "desktop-budget",
  "allowed_models": ["example-chat"],
  "cost_quota_micro": 100000000,
  "rate_limits": [
    {"window": "1h", "request_quota": 0, "token_quota": 0, "cost_quota_micro": 5000000}
  ]
}
```

Key 创建、列表、详情、`GET /api/me` 增加：

```json
{
  "cost_quota_micro": 100000000,
  "used_cost_micro": 12503500,
  "cost_remaining_micro": 87496500,
  "cost_unlimited": false
}
```

`GET /api/me/limits` 的 `lifetime` 同样包含这四个字段；`rate_limit_usage` 继续列出窗口用量。`GET /admin/api-keys/{id}/usage` 新增同结构 `lifetime`，报表不可用时仍提供额度信息与降级标识。

JWT grant 的金额是新 Key 的发放策略，修改 grant 不追改已发放 Key。`/api/apply-apikey` 从 grant 继承金额与窗口额度；`/api/bind-apikey` 从服务端 access-key YAML 继承，调用者不能选择预算。重复绑定不会重置已消费。

## Desktop 余额接口

`GET /api/me/balance` 使用客户端 Key 鉴权，余额来自用量库的实时内存计数，与日志查询无关。耗尽额度的 Key 仍可查询自身信息、余额和限额。

```json
{
  "billing_version": "credits_v1",
  "currency": "CNY",
  "micro_per_credit": 10000,
  "cost_quota_micro": 100000000,
  "used_cost_micro": 100020000,
  "cost_remaining_micro": -20000,
  "cost_unlimited": false,
  "cost_micro": 100020000,
  "unlimited": false,
  "items": [
    {
      "window": "1h",
      "starts_at": "2026-09-23T06:00:00Z",
      "resets_at": "2026-09-23T07:00:00Z",
      "requests": 2,
      "request_quota": 0,
      "request_remaining": 0,
      "tokens": 1000,
      "token_quota": 0,
      "token_remaining": 0,
      "cost_micro": 5020000,
      "cost_quota_micro": 5000000,
      "cost_remaining_micro": -20000
    }
  ],
  "degraded_components": ["telemetry"]
}
```

- `cost_micro` 保留为兼容字段，现在是 `used_cost_micro` 的别名，不再表示日志保留期内 SUM。
- `cost_unlimited` 仅表示生命周期金额不限；`unlimited` 表示生命周期及所有金额窗口均不限。二者不代表请求数或 Token 不限。
- 不限时 `cost_remaining_micro=0` 是占位值，必须结合 `cost_unlimited` 判断。
- `items` 仅含配置了金额额度的窗口，空数组不意味着生命周期不限。
- 各窗口预算同时生效，**不可相加**。总余额、各窗口剩余额度分开展示；如要显示当前可用值，取所有有限额度剩余的最小值并下限截为 0。实际超额仍显示负余额。
- `degraded_components` 可包含 usage、telemetry；前者表示用量持久化降级，后者仅表示报表/日志降级，均不应直接替换为零余额。
- 不同查询不是原子快照，可能有少量在途变化，Desktop 单张余额卡以 balance 一次响应为准。
- 新 Desktop 遇到旧后端缺少 `billing_version`/生命周期字段时应显示“不支持总额度”，不能默认成无限或零消费。
- Key、余额、限额是核心；usage/logs/sessions/prices 可独立失败。不要用一个 `Promise.all` 让报表故障隐藏可用余额，也不要因一个报表故障切换到其他 Key。

## 模型定价

GET/POST `/admin/model-prices`、PATCH `/admin/model-prices/{id}`、GET `/api/me/prices` 增加 `billing`。PATCH 省略价格字段保留原值；显式 `null` 缓存命中价格表示沿用输入价格。`billing` 是整体替换对象。

文本示例（金额单位仍是微元）：

```json
{
  "protocol": "openai",
  "public_model": "example-chat",
  "currency": "CNY",
  "input_cost_micro_per_1m_tokens": 10000000,
  "input_cache_hit_cost_micro_per_1m_tokens": 2000000,
  "output_cost_micro_per_1m_tokens": 30000000,
  "billing": {
    "mode": "tokens",
    "cache_write_cost_micro_per_1m_tokens": 12000000
  }
}
```

输入 Token 统一包含缓存读写；普通输入、缓存命中、缓存写入互斥计算。兼容 OpenAI cached_tokens、DeepSeek cache hit/miss，以及 Anthropic cache read/write 和 SSE message_start/message_delta。OpenAI 流式请求会设置 `stream_options.include_usage=true`；SSE 用量独立扫描，不受 8 MiB 日志采样上限影响。

图片示例：

```json
{
  "protocol": "openai",
  "public_model": "example-image",
  "currency": "CNY",
  "billing": {
    "mode": "image",
    "image_prices": [
      {"size": "", "quality": "", "cost_micro": 100000},
      {"size": "1024x1024", "quality": "hd", "cost_micro": 200000}
    ]
  }
}
```

空选择器是通配符；更具体规则优先。同等具体程度的多条匹配会拒绝请求，避免选错价格。按返回 `data` 数组中的图片数计费，JSON 与 multipart 请求均支持。成功的超大图片响应超出采样上限时按请求 n 估算并标记；不能识别产出数量的响应记为 unavailable，不伪装成精确零消费。

免费必须显式配置 `billing.mode="free"`。无价格的模型在金额受限 Key 下拒绝；未配置金额限制的旧 Key 可继续使用，日志标记 unpriced。旧的全零 token 价格必须改成显式 free。图片可使用 tokens 模式，必须读取上游实际 usage；缺失或响应采样截断导致无法解析 usage 时标记 unavailable，不用 Base64 字节数估算，不扣估算费用。

嵌入模型使用 tokens 模式、仅计算输入。多模态及上游特殊计价规则不自动从人民币价格推导；未提供标准用量时是估算消费，不承诺与上游账单完全一致。

## 请求明细与错误

GET `/admin/logs`、`/admin/api-keys/{id}/logs`、`/api/me/logs` 新增：

- `billing_status`：charged（按已知用量）、estimated（估算）、free（显式免费）、not_charged（未计费失败）、unpriced（无价格）、unavailable（无法计量）、legacy（升级前记录）。
- `price_snapshot`：本次开始时读取的完整价格对象，缺失为 null；不含上游密钥。
- `started_at`：请求开始 UTC 时间，旧记录为 null。
- `cache_write_tokens`、`image_count`：计费明细。

本地拒绝、连接失败、上游非 2xx 响应不扣 Credits；成功请求按 usage 优先，缺失时按字节粗估。成功流式响应中断时按已观察用量或估算扣费，不因断开全额退款。价格修改不改变已开始请求和历史消费。

公共 `/v1/...` 成功响应协议保持兼容。总额度耗尽、窗口耗尽、并发上限为 429；窗口耗尽含 `Retry-After`，总额度耗尽不提供重置时间。价格缺失沿用已有 429 错误；配置全零非 free 为 503；图片规则不合法为 400。不要依赖错误文案解析余额，应查询上述元数据接口。

## SQLite 与升级

保持三个数据库：

| 数据库 | 结构变化 |
|---|---|
| control | api_keys.cost_quota_micro、jwt_grants.cost_quota_micro、model_prices.billing |
| usage | usage_totals.used_cost_micro，schema_migrations 记录 credits_v1 |
| telemetry | request_logs 增加五个计费元数据字段，schema_migrations 记录 credits_v1 |

启动幂等迁移。旧 Key/grant 总金额额度默认 0；旧生命周期消费从 0 开始累计，不从历史日志追扣；当前金额窗口保持已有消费。旧日志标记 legacy，旧价格默认为 tokens 模式。

拆分/回迁工具保留新日志字段和累计金额。回迁工具在旧控制库 api_keys 写入 used_cost_micro 快照供再次拆分使用，运行时不维护该副本；运行时权威金额仍在 usage 库。

升级前备份三个数据库。先发布 Server，再发布 Website，Desktop 后续按本契约接入。检查 CNY 价格、显式免费和图片计费配置后再开放预算 Key。旧客户端可继续访问原路径，但旧 Desktop 的窗口余额相加算法仍需自行升级。

### 并发限制错误

`MAX_CONCURRENT_PER_KEY` 是进程级统一配置，按 API Key 分别计数；同一 Key 的所有模型与代理接口共享上限，不支持单 Key 覆盖，多实例间不共享计数。默认每个 Key 16 个在途请求。

触发时返回 HTTP 429，保留字符串 `error`，新增 `code: "api_key_concurrency_limit_exceeded"`、`scope: "api_key"` 和 `retryable: true`。调用端应优先识别结构化 code，不应将全部 429 归为额度耗尽；等待在途请求完成后退避重试。

### Token 阶梯价格

`billing.token_tiers` 可配置按总输入 tokens 选择的整请求价格，阈值严格递增；仅在输入数量大于 `above_input_tokens` 时应用。字段为 `input_cost_micro_per_1m_tokens`、`output_cost_micro_per_1m_tokens` 和可选 `input_cache_hit_cost_micro_per_1m_tokens`。不是累进分段计费，缓存输入也计入阈值；缓存单价未设置时回退到该阶梯输入价。管理页展示并在编辑基础价时保留阶梯配置。
