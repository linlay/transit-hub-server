# Credits 计费与 Desktop API 契约

本版本由 Transit Hub Server、管理 Website 实现。Desktop 代码不在本次改动范围内。

## 单位与范围

- 唯一计费单位为 `CREDITS`；`1 Credit = 1,000,000 micro-Credits`。没有币种选择或运行时汇率。
- SQLite 使用 int64 micro-Credits；API 中所有金额字段使用十进制整数字符串，前端除以 1,000,000 显示 Credits，支持六位小数。Website 对超出 JavaScript 安全整数范围的金额明确报错，不静默舍入。
- 生命周期预算按 API Key 独立管理，多个 Key 不自动合并成用户预算。
- 不再读取 `CURRENCY`。价格可省略 `unit`；提供时只能为 `CREDITS`。旧金额字段和旧 `currency` 写入请求会被拒绝。当前契约直接修订，不新增 v2 路径或 `billing_version`。
- 金额计算各项求和后四舍五入到micro-Credits，整数乘法使用大整数避免中间溢出，计数溢出饱和为 int64 最大值。
- 此功能不是充值钱包；修改总额度是修改生命周期消费上限，不清空已消费。

## 软额度与窗口

请求开始检查：总金额、窗口金额、既有请求数/Token 限额均未耗尽才放行。完成后短锁更新内存计数，后台异步落盘，不预占、不冻结、不跨请求持有事务。已放行请求不会因余额耗尽被中断，因此消费和剩余可超过额度、低于零。

`quota_microcredits=0` 表示不限。负配额拒绝。PATCH 省略字段不修改，传 `0` 取消限制。

5h 和 7d 为每个 Key 独立的首次使用窗口：请求通过鉴权、权限、价格、并发和额度检查，准备发送上游时启动，分别持续 5 小时和 168 小时。到期后等待下一次接纳请求重新开启，空闲不续期。两个周期独立检查，任一耗尽则拒绝；拒绝请求不启动窗口，上游失败不撤销窗口。额度仍为完成后记账的软额度，不预占。

Usage 库新增 `usage_windows(api_key_id, window, window_start, window_end, updated_at)`，主键为 `(api_key_id, window)`；开窗同步事务持久化，失败时新请求返回 503，避免重启后凭空恢复额度。计数继续异步写入 `usage_buckets`，累计用量保存在 `usage_totals`。请求接纳时绑定窗口起点，长请求完成后仍归原窗口。当前架构为单实例，不支持多个进程共享用量库进行额度协调。

上线不继承旧 5h/7d 周期用量，旧桶保留但不参与新窗口统计，累计用量不清空。未开窗时 API 返回 `state: idle`，到期未续期时返回 `state: expired`，两者当前周期用量均为 0；活动窗口返回 `state: active`。idle 的起止时间为 Go 零值，客户端必须按 state 展示“首次使用后开始”。7d 不再按自然周计算。1h、1d、30d 保持原规则，使用 `RATE_LIMIT_TIMEZONE`（默认 Asia/Shanghai）。日志 `created_at` 仍是完成时间；`started_at` 为已接纳请求的接纳时间。

`MAX_CONCURRENT_PER_KEY` 默认 16，必须为正整数；仅限制同时在途请求，不预占金额。计费不补充、修改或限制客户端输出额度；未指定时由上游处理，指定时原样透传。旧价格 JSON 中的 `default_max_output_tokens` 和 `max_output_tokens` 已废弃并忽略，无需迁移数据库。图片 `n` 为 1–10。超额没有固定金额保证，取决于单次费用与并发数。

用量库异常时维持单实例内存计数并在恢复后补写；异常退出可能丢失尚未落盘用量。禁止多个独立实例共同执行同一 Key 的预算。日志为尽力记录，可丢弃、定期清理，不能用日志 SUM 重建权威余额。

## Key 配置与响应

以下管理员写接口增加 `quota_microcredits`：

- POST `/admin/api-keys`
- PATCH `/admin/api-keys/{id}`
- POST `/admin/jwt-grants`
- PATCH `/admin/jwt-grants/{jti}`

示例：总额度 10,000 Credits，每小时 500 Credits。

```json
{
  "name": "desktop-budget",
  "allowed_models": ["example-chat"],
  "quota_microcredits": "10000000000",
  "rate_limits": [
    {"window": "1h", "request_quota": 0, "token_quota": 0, "quota_microcredits": "500000000"}
  ]
}
```

Key 创建、列表、详情、`GET /api/me` 增加：

```json
{
  "quota_microcredits": "10000000000",
  "used_microcredits": "1250350000",
  "remaining_microcredits": "8749650000",
  "credits_unlimited": false
}
```

`GET /api/me/limits` 的 `lifetime` 同样包含这四个字段；`rate_limit_usage` 继续列出窗口用量。`GET /admin/api-keys/{id}/usage` 新增同结构 `lifetime`，报表不可用时仍提供额度信息与降级标识。

JWT grant 的金额是新 Key 的发放策略，修改 grant 不追改已发放 Key。`/api/apply-apikey` 从 grant 继承金额与窗口额度；`/api/bind-apikey` 从服务端 access-key YAML 继承，调用者不能选择预算。重复绑定不会重置已消费。

## Desktop 余额接口

`GET /api/me/balance` 使用客户端 Key 鉴权，余额来自用量库的实时内存计数，与日志查询无关。耗尽额度的 Key 仍可查询自身信息、余额和限额。

```json
{
  "unit": "CREDITS",
  "microcredits_per_credit": 1000000,
  "quota_microcredits": "10000000000",
  "used_microcredits": "10002000000",
  "remaining_microcredits": "-2000000",
  "credits_unlimited": false,
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
      "charged_microcredits": "502000000",
      "quota_microcredits": "500000000",
      "remaining_microcredits": "-2000000"
    }
  ],
  "degraded_components": ["telemetry"]
}
```

- `used_microcredits` 来自权威累计用量，不是日志保留期内 SUM。
- `credits_unlimited` 仅表示生命周期金额不限；`unlimited` 表示生命周期及所有金额窗口均不限。二者不代表请求数或 Token 不限。
- 不限时 `remaining_microcredits=0` 是占位值，必须结合 `credits_unlimited` 判断。
- `items` 仅含配置了金额额度的窗口，空数组不意味着生命周期不限。
- 各窗口预算同时生效，**不可相加**。总余额、各窗口剩余额度分开展示；如要显示当前可用值，取所有有限额度剩余的最小值并下限截为 0。实际超额仍显示负余额。
- `degraded_components` 可包含 usage、telemetry；前者表示用量持久化降级，后者仅表示报表/日志降级，均不应直接替换为零余额。
- 不同查询不是原子快照，可能有少量在途变化，Desktop 单张余额卡以 balance 一次响应为准。
- 新 Desktop 遇到旧后端缺少 `unit: CREDITS` 或生命周期字段时应显示“不支持总额度”，不能默认成无限或零消费。
- Key、余额、限额是核心；usage/logs/sessions/prices 可独立失败。不要用一个 `Promise.all` 让报表故障隐藏可用余额，也不要因一个报表故障切换到其他 Key。

## 模型定价

GET/POST `/admin/model-prices`、PATCH `/admin/model-prices/{id}`、GET `/api/me/prices` 增加 `billing`。PATCH 省略价格字段保留原值；显式 `null` 缓存命中价格表示沿用输入价格。`billing` 是整体替换对象。

文本示例（金额单位仍是micro-Credits）：

```json
{
  "protocol": "openai",
  "public_model": "example-chat",
  "unit": "CREDITS",
  "input_microcredits_per_1m_tokens": "1000000000",
  "input_cache_hit_microcredits_per_1m_tokens": "200000000",
  "output_microcredits_per_1m_tokens": "3000000000",
  "billing": {
    "mode": "tokens",
    "cache_write_microcredits_per_1m_tokens": "1200000000"
  }
}
```

输入 Token 统一包含缓存读写；普通输入、缓存命中、缓存写入互斥计算。兼容 OpenAI cached_tokens、DeepSeek cache hit/miss，以及 Anthropic cache read/write 和 SSE message_start/message_delta。OpenAI 流式请求会设置 `stream_options.include_usage=true`；SSE 用量独立扫描，不受 8 MiB 日志采样上限影响。

图片示例：

```json
{
  "protocol": "openai",
  "public_model": "example-image",
  "unit": "CREDITS",
  "billing": {
    "mode": "image",
    "image_prices": [
      {"size": "", "quality": "", "charged_microcredits": "10000000"},
      {"size": "1024x1024", "quality": "hd", "charged_microcredits": "20000000"}
    ]
  }
}
```

空选择器是通配符；更具体规则优先。同等具体程度的多条匹配会拒绝请求，避免选错价格。按返回 `data` 数组中的图片数计费，JSON 与 multipart 请求均支持。成功的超大图片响应超出采样上限时按请求 n 估算并标记；不能识别产出数量的响应记为 unavailable，不伪装成精确零消费。

免费必须显式配置 `billing.mode="free"`。无价格的模型在金额受限 Key 下拒绝；未配置金额限制的旧 Key 可继续使用，日志标记 unpriced。旧的全零 token 价格必须改成显式 free。图片可使用 tokens 模式，必须读取上游实际 usage；缺失或响应采样截断导致无法解析 usage 时标记 unavailable，不用 Base64 字节数估算，不扣估算费用。

嵌入模型使用 tokens 模式、仅计算输入。多模态及上游特殊计价规则需显式配置 Credits 单价；未提供标准用量时是估算消费，不承诺与上游账单完全一致。

## 请求明细与错误

GET `/admin/logs`、`/admin/api-keys/{id}/logs`、`/api/me/logs` 新增：

- `billing_status`：charged（按已知用量）、estimated（估算）、free（显式免费）、not_charged（未计费失败）、unpriced（无价格）、unavailable（无法计量）、legacy（升级前记录）。
- `price_snapshot`：本次开始时读取的完整价格对象，缺失为 null；不含上游密钥。
- `started_at`：请求开始 UTC 时间，旧记录为 null。
- `cache_write_tokens`、`image_count`：计费明细。

本地拒绝、连接失败、上游非 2xx 响应不扣 Credits；成功请求按 usage 优先，缺失时按字节粗估。成功流式响应中断时按已观察用量或估算扣费，不因断开全额退款。价格修改不改变已开始请求和历史消费。

Responses `/v1/responses` 复用 openai 模型价格：非流式读取顶层 `usage`，流式读取 `response.usage`，按累计快照更新而非逐事件相加。`input_tokens_details.cached_tokens` 使用缓存价，`output_tokens` 已包含推理 token，不重复加算。显式零输入/输出用量视为已计量。

Responses 缺少可解析用量（含断流、单条 SSE 数据行超过 8 MiB、非流式 JSON 超过 8 MiB 采样上限）时，不按请求密文或重复输出快照的字节数估算：token 和费用记为 0，token 价格的 2xx 请求记录 `billing_status: unavailable`；free 模式保持 free。请求数仍累计，不做后续用量补结算。有权威用量时按已观察用量结算；HTTP 非 2xx 仍不扣 Credits。SSE 的失败/未完成事件原样透传，HTTP 状态保持上游值，网关不把业务失败事件改写为 HTTP 错误。

公共 `/v1/...` 成功响应协议保持兼容。总额度耗尽、窗口耗尽、并发上限为 429；窗口耗尽含 `Retry-After`，总额度耗尽不提供重置时间。价格缺失沿用已有 429 错误；配置全零非 free 为 503；图片规则不合法为 400。不要依赖错误文案解析余额，应查询上述元数据接口。

## SQLite 与升级

control 保存模型定价、Key/JWT 额度；usage 保存权威生命周期和窗口用量；telemetry 保存请求消费和价格快照。所有金额列统一为 `*_microcredits`（模型单价列为 `*_microcredits_per_1m`）。

旧数据库必须先离线迁移。运行时检测到旧金额列会拒绝启动，即使用量库允许降级也不会将旧币种当作新单位使用。`schema_migrations.native_credits` 仅是幂等迁移记录，不是公共 API 版本。新数据库直接建立原生 Credits schema。

1. 停止服务并等待在途请求和用量刷盘完成。
2. 对每个存在的数据库传入 `--db` 运行预检；不存在的库不要传入。
3. 同样的命令增加 `--apply`。脚本先备份所有数据库，再转换；输出备份目录与行数报告。
4. 同步更新 access-key/issuer YAML 的额度字段；旧整数乘以 100。JSON 请求金额必须为字符串，YAML/SQLite 使用整数。
5. 同步发布 Server、Website 和消费额度接口的客户端。不会提供人民币兼容响应。

```sh
python3 scripts/migrate_native_credits.py --db data/transit-hub.db --db data/transit-hub-usage.db --db data/transit-hub-telemetry.db
# 确认预检后，追加 --apply 执行。
```

历史整数统一乘以 100，保持旧页面显示的 Credits 不变；包含嵌套缓存、阶梯、图片规则及历史价格快照。非 CNY 旧价格、未知快照、混合单位及溢出会使迁移失败。总消费不得从日志重新汇总。迁移不改变窗口时间、Token 数和请求数，不清空用量。

三个库分别提交事务，发布期间保持停机；若提交阶段发生故障，检查各库迁移标记并幂等重跑，或整体恢复备份，不允许混合单位运行。恢复服务并产生新消费后，不可直接用旧备份覆盖。

旧的合库如需拆库，应先运行原生 Credits 迁移，再使用现有 split 工具。split/merge 工具传递原生 Credits，不做汇率转换。

### 并发限制错误

`MAX_CONCURRENT_PER_KEY` 是进程级统一配置，按 API Key 分别计数；同一 Key 的所有模型与代理接口共享上限，不支持单 Key 覆盖，多实例间不共享计数。默认每个 Key 16 个在途请求。

触发时返回 HTTP 429，保留字符串 `error`，新增 `code: "api_key_concurrency_limit_exceeded"`、`scope: "api_key"` 和 `retryable: true`。调用端应优先识别结构化 code，不应将全部 429 归为额度耗尽；等待在途请求完成后退避重试。

### Token 阶梯价格

`billing.token_tiers` 可配置按总输入 tokens 选择的整请求价格，阈值严格递增；仅在输入数量大于 `above_input_tokens` 时应用。字段为 `input_microcredits_per_1m_tokens`、`output_microcredits_per_1m_tokens` 和可选 `input_cache_hit_microcredits_per_1m_tokens`。不是累进分段计费，缓存输入也计入阈值；缓存单价未设置时回退到该阶梯输入价。管理页展示并在编辑基础价时保留阶梯配置。
