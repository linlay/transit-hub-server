-- Source: zenmind-env/registries.example/models/deepseek-flash.yml (2026-09-16).
-- CNY per 1M tokens: input 2.0, cache hit 0.04, output 8.0.
INSERT INTO model_prices (
  id, protocol, public_model, input_cost_micro_per_1m,
  input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m,
  currency, created_at, updated_at
) VALUES (
  'price_deepseek_flash', 'openai', 'deepseek-flash', 2000000,
  40000, 8000000, 'CNY', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
  strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
)
ON CONFLICT(protocol, public_model) DO UPDATE SET
  input_cost_micro_per_1m = excluded.input_cost_micro_per_1m,
  input_cache_hit_cost_micro_per_1m = excluded.input_cache_hit_cost_micro_per_1m,
  output_cost_micro_per_1m = excluded.output_cost_micro_per_1m,
  currency = excluded.currency,
  updated_at = excluded.updated_at;
