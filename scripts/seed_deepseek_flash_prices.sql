-- Native Credits tariff. Integer micro-Credits per million tokens (1 Credit = 1000000).
-- Preserves the visible Credits prices at the native billing migration; no runtime exchange rate.
INSERT INTO model_prices (
  id, protocol, public_model, input_microcredits_per_1m,
  input_cache_hit_microcredits_per_1m, output_microcredits_per_1m,
  unit, created_at, updated_at
) VALUES (
  'price_deepseek_flash', 'openai', 'deepseek-flash', 200000000, 4000000, 800000000, 'CREDITS', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
  strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
)
ON CONFLICT(protocol, public_model) DO UPDATE SET
  input_microcredits_per_1m = excluded.input_microcredits_per_1m,
  input_cache_hit_microcredits_per_1m = excluded.input_cache_hit_microcredits_per_1m,
  output_microcredits_per_1m = excluded.output_microcredits_per_1m,
  unit = excluded.unit,
  updated_at = excluded.updated_at;
