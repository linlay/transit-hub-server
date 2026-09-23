# 本地模型价格补充（2026-09-23）

来源为 Babelark 公开目录 https://babelark.ai/api/pricing 及模型详情页；图片按张报价来自该站详情页使用的公开前端价格表。使用默认分组，不应用 VIP/SVIP 折扣。人民币转换沿用仓库既有固定策略 USD/CNY=7，并非实时汇率。MiMo V2.6 价格由用户本次提供。

执行 `scripts/seed_model_price_supplements.sql`，要求先使用支持 `billing.token_tiers` 和图片 tokens 计费的本版本后端。脚本以协议和公开模型名 upsert，保留已有记录 ID 和创建时间，不修改其他模型价格、已记账用量或密钥配额。旧本地库须先由新版本 Store.Open 完成 schema 升级，确保 model_prices.billing 存在。

## Token 价格

单位：人民币元 / 百万 tokens。

| 公开模型 | 输入 | 缓存读取 | 输出 |
|---|---:|---:|---:|
| gpt-5_6-luna | 1.4 | 0.14 | 8.4 |
| gpt-5_6-terra | 14 | 1.4 | 84 |
| gpt-image-2 | 35 | 8.75 | 210 |
| gpt-image-2_5-flare | 35 | 8.75 | 210 |
| gpt-image-2_5-sunburst | 35 | 8.75 | 210 |
| gemini-3_1-flash-lite-image | 1.75 | 0.175 | 210 |
| mimo-v2_6-pro | 3 | 0.025 | 6 |
| mimo-v2_6-flash | 1 | 0.02 | 2 |

Luna 输入超过 272000 tokens 后，整请求输入/缓存/输出价为 2.8 / 0.28 / 12.6；Terra 为 28 / 2.8 / 126。Babelark 目录的 input_ratio×2 为美元每百万输入价；输出乘 completion_ratio，缓存乘 cache_ratio；高阶价格按对应 input_ratio/output_ratio 换算。

来源：[Flare](https://babelark.ai/models/gpt-image-2.5-flare)、[Sunburst](https://babelark.ai/models/gpt-image-2.5-sunburst)、[Flash Lite Image](https://babelark.ai/models/gemini-3.1-flash-lite-image)、[Luna](https://babelark.ai/models/gpt-5.6-luna)、[Terra](https://babelark.ai/models/gpt-5.6-terra)。

图片 tokens 模式仅按可解析的实际 usage 计费。上游缺失 usage 或大型响应超出日志采样范围导致无法解析时，记录 unavailable；不按 URL/Base64 的文本长度估算，也不承诺与上游账单完全一致。

## Flash Image Preview 按张价格

[站点详情](https://babelark.ai/models/gemini-3.1-flash-image-preview)展示 512px=$0.045、1K=$0.067、2K=$0.101、4K=$0.151。

本地仅映射明确的方形尺寸：512x512=¥0.315、1024x1024=¥0.469、2048x2048=¥0.707、4096x4096=¥1.057。不配置默认通配尺寸；非方形、auto 或省略 size 暂不推断分辨率档位，以免误扣。quality 不影响该分辨率报价。

## 用户确认与路由清理

用户确认 gpt-image-2（上游 gpt-image-2-t）与 Image 2.5 使用相同计费价格，因此按相同 tokens 价格补齐；上游映射不变。

用户要求删除 grok-4.5。本地 Orange 配置仅包含该模型，已移出运行时 provider 目录并备份；清理脚本同时删除其价格记录，历史用量不删除。

本次不新增站点上尚未配置的模型路由，不改已有厂商原价策略及 MiniMax Coding Plan 内部优惠。
