-- Seed model prices for common Transit Hub public model names.
-- Currency: CNY, stored as micro-CNY per 1M tokens.
-- Source checked on 2026-06-16:
--   DeepSeek: https://api-docs.deepseek.com/zh-cn/quick_start/pricing
--   MiniMax: https://platform.minimaxi.com/docs/guides/pricing-paygo
-- Review provider billing pages before production import; upstream prices can change.
-- Timestamp: 2026-06-16T00:00:00Z

INSERT OR REPLACE INTO model_prices (id, protocol, public_model, input_cost_micro_per_1m, input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m, currency, created_at, updated_at)
VALUES
  -- DeepSeek via Transit Hub
  ('price_seed_001', 'openai', 'deepseek-v4-flash',          1000000,   20000,   2000000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_002', 'openai', 'deepseek-v4-pro',            3000000,   25000,   6000000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  -- Compatibility aliases, deprecated by DeepSeek after 2026-07-24 23:59 Beijing time.
  ('price_seed_003', 'openai', 'deepseek-chat',              1000000,   20000,   2000000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_004', 'openai', 'deepseek-reasoner',          1000000,   20000,   2000000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),

  -- MiniMax via Transit Hub
  ('price_seed_005', 'openai', 'minimax-m3-openai',             2100000,  420000,   8400000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_006', 'openai', 'minimax-m3-long-openai',        4200000,  840000,  16800000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_007', 'openai', 'minimax-m3-priority-openai',    3150000,  630000,  12600000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_008', 'openai', 'minimax-m3-long-priority-openai', 6300000,1260000, 25200000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_009', 'openai', 'minimax-m2_7-openai',           2100000,  420000,   8400000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_010', 'openai', 'minimax-m2_7-highspeed-openai', 4200000,  420000,  16800000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_011', 'openai', 'minimax-m2_5-openai',           2100000,  210000,   8400000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_012', 'openai', 'minimax-m2_5-highspeed-openai', 4200000,  210000,  16800000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_013', 'openai', 'minimax-m2_1-openai',           2100000,  210000,   8400000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_014', 'openai', 'minimax-m2_1-highspeed-openai', 4200000,  210000,  16800000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_015', 'openai', 'minimax-m2-openai',             2100000,  210000,   8400000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),

  -- XiaoMi Mimo via Transit Hub
  ('price_seed_016', 'openai', 'mimo-v2_5',                     1000000,   20000,   2000000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z'),
  ('price_seed_017', 'openai', 'mimo-v2_5-pro',                 3000000,   25000,   6000000, 'CNY', '2026-06-16T00:00:00Z', '2026-06-16T00:00:00Z');
