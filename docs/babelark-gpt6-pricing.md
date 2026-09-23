# Babelark GPT-6 模型（2026-09-23）

公开名和上游名均为 `gpt-6-luna`、`gpt-6-sol`，使用 `primary` 账号池及 `/v1/chat/completions`。价格来源：[Luna](https://babelark.ai/models/gpt-6-luna)、[Sol](https://babelark.ai/models/gpt-6-sol)、[公开价格接口](https://babelark.ai/api/pricing)。沿用项目固定 USD/CNY=7，默认用户分组，不含 VIP 折扣。

单位：人民币元 / 百万 tokens。

| 模型 | 输入 | 缓存读取 | 输出 | 输入超过 272000 后的输入 / 缓存 / 输出 |
|---|---:|---:|---:|---|
| gpt-6-luna | 0.7 | 0.07 | 3.5 | 1.4 / 0.14 / 5.25 |
| gpt-6-sol | 14 | 1.4 | 70 | 28 / 2.8 / 105 |

执行 `sqlite3 data/transit-hub.db < scripts/seed_babelark_gpt6_prices.sql` 幂等导入；需使用支持 token_tiers 的后端。真实 provider 的 models 段也需要加入上述两个模型，随后重载 provider。模板已更新；不更换任何环境的凭据，不调整 API Key 模型白名单。
