# BabelArk Claude 5.5 接入

三个模型通过独立的 `babelark-anthropic` provider 接入，协议为 `anthropic`，客户端和上游路径均为 `POST /v1/messages`。Provider 配置只支持一种协议，因此使用独立配置复用 BabelArk 账号，原有 `babelark` OpenAI 路由继续使用原配置。

| 公开模型 | 上游模型 | 显示名称 |
| --- | --- | --- |
| `claude-haiku-5_5` | `claude-haiku-5-5` | Claude Haiku 5.5 |
| `claude-opus-5_5` | `claude-opus-5-5` | Claude Opus 5.5 |
| `claude-sonnet-5_5` | `claude-sonnet-5-5` | Claude Sonnet 5.5 |

配置依据为 `zenmind-env/registries/models/babelark-claude-{haiku,opus,sonnet}-5_5.yml`。三个登记文件提供基础价格、1M 输入和 128K 输出规格，并按 BabelArk 渠道限制登记为仅文本输入。Transit Hub 的 provider schema 不包含上下文长度、视觉或推理能力开关，实际能力和限制由上游执行。

## 配置与价格

复制 `configs/providers/babelark-anthropic.example.yaml` 为 `configs/providers/babelark-anthropic.yaml`，通过 `BABELARK_API_KEY` 注入与现有 BabelArk 相同的凭据。现有配置使用内联 Key 时，可以在真实配置中复用相同账号池；不要将真实配置提交到 Git。默认上游鉴权为 `x-api-key`，模板同时设置 `anthropic-version: 2023-06-01`。

执行 `sqlite3 data/transit-hub.db < scripts/seed_babelark_claude_prices.sql` 导入价格，自定义 Control DB 路径需替换。脚本按协议和公开模型名幂等更新，保留已有记录 ID 和创建时间。价格按请求读取；模型路由需重启服务或调用 `POST /admin/providers/reload` 生效。受模型白名单限制的客户端 Key 需包含对应公开模型名。

下表为每百万 tokens 的静态 Credits 价格，沿用仓库已有 BabelArk 定价尺度：来源登记中的 1 元对应 100 Credits，写入 SQLite 时再乘 1,000,000 转成 micro-Credits；运行时不使用汇率。

| 公开模型 | 输入 | 缓存读取 | 输出 |
| --- | ---: | ---: | ---: |
| `claude-haiku-5_5` | 70 | 7 | 350 |
| `claude-opus-5_5` | 2800 | 140 | 14000 |
| `claude-sonnet-5_5` | 1400 | 70 | 7000 |

Haiku 总输入超过 100,000 tokens 时，整请求输入 / 缓存读取 / 输出价为 350 / 35 / 1750 Credits。输入和输出阶梯来自登记文件注释；缓存阶梯按 [Haiku 官方规格](https://platform.claude.com/docs/en/models/haiku-5-5/overview) 的 $0.05/MTok 与登记文件固定 USD/CNY=7 尺度补齐。缓存输入计入阶梯阈值。登记文件未提供缓存写入溢价，当前缓存写入按所选阶梯的普通输入价计算，因此此表不代表上游缓存写入账单的精确复刻。BabelArk 页面未能通过本次网页读取核实，渠道价格以用户提供的登记文件为依据。

## 请求与协议边界

Transit Hub 已实现 Anthropic Messages JSON/SSE 原生透传：只改写 `model`，保留 `system`、`thinking`、`output_config`、`tools`、`tool_choice`、内容块和客户端 `anthropic-beta` 等字段或请求头。响应体和 SSE 事件原样返回，统计 `input_tokens`、`output_tokens`、`cache_read_input_tokens`、`cache_creation_input_tokens`。

调用方需使用模型支持的参数。5.5 系列不接受旧的 `thinking.type=enabled` 加 `budget_tokens`，可使用 `thinking.type=adaptive` 和 `output_config.effort`；Opus 5.5 的 adaptive thinking 始终开启。详见 [Haiku 迁移指南](https://platform.claude.com/docs/en/models/haiku-5-5/migration-guide)、[Opus 迁移指南](https://platform.claude.com/docs/en/models/opus-5-5/migration-guide)、[Sonnet 迁移指南](https://platform.claude.com/docs/en/models/sonnet-5-5/migration-guide)。Transit Hub 不自动修正不兼容参数，也不进行 OpenAI 与 Anthropic 请求转换。

```bash
curl "$TRANSIT_HUB_URL/v1/messages" \
  -H "x-api-key: $CLIENT_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-haiku-5_5",
    "max_tokens": 4096,
    "thinking": {"type": "adaptive"},
    "output_config": {"effort": "medium"},
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

这次接入使用现有 Messages 代理能力；不包含 Token Counting、Message Batches 等其他 Anthropic API，也未执行真实上游生成请求。
