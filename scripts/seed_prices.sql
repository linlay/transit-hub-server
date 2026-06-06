-- Seed model prices from registry YAML files
-- Currency: CNY, stored as micro-CNY per 1M tokens
-- Timestamp: 2026-06-06T04:04:55Z

INSERT OR REPLACE INTO model_prices (id, protocol, public_model, input_cost_micro_per_1m, input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m, currency, created_at, updated_at)
VALUES
  -- DeepSeek via Transit Hub
  ('price_seed_001', 'openai', 'deepseek-v4-flash',         1000000,  20000,   2000000, 'CNY', '2026-06-06T04:04:55Z', '2026-06-06T04:04:55Z'),
  ('price_seed_002', 'openai', 'deepseek-v4-pro',           3000000,  25000,   6000000, 'CNY', '2026-06-06T04:04:55Z', '2026-06-06T04:04:55Z'),

  -- MiniMax via Transit Hub
  ('price_seed_003', 'openai', 'minimax-m3-openai',         2100000,  420000,  8400000, 'CNY', '2026-06-06T04:04:55Z', '2026-06-06T04:04:55Z'),
  ('price_seed_004', 'openai', 'minimax-m3-long-openai',    8400000,  1680000, 33600000, 'CNY', '2026-06-06T04:04:55Z', '2026-06-06T04:04:55Z'),
  ('price_seed_005', 'openai', 'minimax-m2_7-openai',       15000000, 15000000,45000000, 'CNY', '2026-06-06T04:04:55Z', '2026-06-06T04:04:55Z'),
  ('price_seed_006', 'openai', 'minimax-m2_7-highspeed-openai', 15000000, 15000000,45000000, 'CNY', '2026-06-06T04:04:55Z', '2026-06-06T04:04:55Z'),

  -- XiaoMi Mimo via Transit Hub
  ('price_seed_007', 'openai', 'mimo-v2_5',                 1000000,  20000,   2000000, 'CNY', '2026-06-06T04:04:55Z', '2026-06-06T04:04:55Z'),
  ('price_seed_008', 'openai', 'mimo-v2_5-pro',             3000000,  25000,   6000000, 'CNY', '2026-06-06T04:04:55Z', '2026-06-06T04:04:55Z');