-- Native Credits tariff. Integer micro-Credits per million tokens (1 Credit = 1000000).
-- Preserves the visible Credits prices at the native billing migration; no runtime exchange rate.

INSERT OR REPLACE INTO model_prices (id, protocol, public_model, input_microcredits_per_1m, input_cache_hit_microcredits_per_1m, output_microcredits_per_1m, unit, created_at, updated_at)
VALUES
  ('price_seed_001', 'openai', 'deepseek-v4-flash',          300000000, 10000000, 900000000, 'CREDITS', '2026-08-18T00:00:00Z', '2026-08-18T00:00:00Z'),
  ('price_seed_002', 'openai', 'deepseek-v4-pro',            900000000, 30000000, 2700000000, 'CREDITS', '2026-08-18T00:00:00Z', '2026-08-18T00:00:00Z'),

  ('price_seed_005', 'openai', 'minimax-m3-openai',              21000000, 4200000, 84000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-09-23T00:00:00Z'),
  ('price_seed_006', 'openai', 'minimax-m3-long-openai',        420000000, 84000000, 1680000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_seed_007', 'openai', 'minimax-m3-priority-openai',    315000000, 63000000, 1260000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_seed_008', 'openai', 'minimax-m3-long-priority-openai', 630000000, 126000000, 2520000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_seed_009', 'openai', 'minimax-m2_7-openai',           210000000, 42000000, 840000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_seed_010', 'openai', 'minimax-m2_7-highspeed-openai', 420000000, 42000000, 1680000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),

  ('price_seed_017', 'openai', 'mimo-v2_5-pro',                 300000000, 2500000, 600000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),

  ('price_seed_018', 'anthropic', 'minimax-m2_7-highspeed-anthropic',
                                                               420000000, 42000000, 1680000000, 'CREDITS', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z');
