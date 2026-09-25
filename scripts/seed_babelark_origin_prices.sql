-- Native Credits tariff. Integer micro-Credits per million tokens (1 Credit = 1000000).
-- Preserves the visible Credits prices at the native billing migration; no runtime exchange rate.

INSERT OR REPLACE INTO model_prices (
  id,
  protocol,
  public_model,
  input_microcredits_per_1m,
  input_cache_hit_microcredits_per_1m,
  output_microcredits_per_1m,
  unit,
  created_at,
  updated_at
)
VALUES
  ('price_babelark_glm5_2',          'openai', 'glm5_2',          800000000, 200000000, 2800000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_babelark_qwen3_7_max',     'openai', 'qwen3_7-max',    1200000000, 240000000, 3600000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_babelark_kimi_k2_7_code',  'openai', 'kimi-k2_7-code',  665000000, 133000000, 2800000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_babelark_embedding_v4',   'openai', 'text-embedding-v4', 50000000, NULL, 0, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z');
