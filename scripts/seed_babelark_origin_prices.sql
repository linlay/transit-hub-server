-- Seed original manufacturer list-price estimates for the BabelArk routes.
-- Currency: CNY, stored as micro-CNY per 1M tokens.
-- Cost estimates intentionally use manufacturer list prices, even when BabelArk
-- settlement uses a Token Plan or another discounted commercial arrangement.
-- Source checked on 2026-07-20:
--   GLM-5.2: https://bigmodel.cn/pricing
--   qwen3.7-max: https://help.aliyun.com/zh/model-studio/model-pricing
--   Kimi K2.7 Code: https://platform.kimi.ai/
--   text-embedding-v4: https://help.aliyun.com/zh/model-studio/embedding
-- USD-denominated Kimi list prices use the fixed policy USD/CNY = 7.0000.
-- qwen3.7-max cache read uses Alibaba Cloud's implicit-cache list-price rule:
-- 20% of the standard input price. Cache creation is not separately representable.
-- Gemini 3.1 Flash Image is deliberately not included: its image output is billed
-- per generated image/resolution, which this token-only schema cannot estimate.

INSERT OR REPLACE INTO model_prices (
  id,
  protocol,
  public_model,
  input_cost_micro_per_1m,
  input_cache_hit_cost_micro_per_1m,
  output_cost_micro_per_1m,
  currency,
  created_at,
  updated_at
)
VALUES
  ('price_babelark_glm5_2',          'openai', 'glm5_2',          8000000, 2000000, 28000000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_babelark_qwen3_7_max',     'openai', 'qwen3_7-max',    12000000, 2400000, 36000000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_babelark_kimi_k2_7_code',  'openai', 'kimi-k2_7-code',  6650000, 1330000, 28000000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_babelark_embedding_v4',   'openai', 'text-embedding-v4', 500000, NULL,        0, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z');
