-- Seed prices for the Bailian Token Plan routes exposed by Transit Hub.
-- Currency: CNY, stored as micro-CNY per 1M tokens.
-- These are the regular Bailian API reference prices, not Token Plan Credits.
-- Source checked on 2026-07-16: https://help.aliyun.com/zh/model-studio/model-pricing
-- qwen3.7-max: the regular 12/36 CNY prices are recorded; a time-limited API
-- promotion may temporarily reduce the actual billed amount.
-- qwen3.7-plus: values are the first API tier (input <= 256K); longer requests
-- have a higher tier that the current single-price schema cannot represent.

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
  ('price_bailian_qwen3_7_max',       'openai', 'bailian-qwen3_7-max',       12000000, NULL, 36000000, 'CNY', '2026-07-16T00:00:00Z', '2026-07-16T00:00:00Z'),
  ('price_bailian_qwen3_7_plus',      'openai', 'bailian-qwen3_7-plus',      2000000, NULL,  8000000, 'CNY', '2026-07-16T00:00:00Z', '2026-07-16T00:00:00Z'),
  ('price_bailian_glm5_2',            'openai', 'bailian-glm5_2',            8000000, NULL, 28000000, 'CNY', '2026-07-16T00:00:00Z', '2026-07-16T00:00:00Z'),
  ('price_bailian_kimi_k2_7_code',    'openai', 'bailian-kimi-k2_7-code',    6500000, NULL, 27000000, 'CNY', '2026-07-16T00:00:00Z', '2026-07-16T00:00:00Z');
