-- Seed model prices for common Transit Hub public model names.
-- Currency: CNY, stored as micro-CNY per 1M tokens.
-- Reference sources:
--   DeepSeek: https://api-docs.deepseek.com/zh-cn/quick_start/pricing
--   MiniMax: https://platform.minimaxi.com/docs/guides/pricing-paygo
--   Xiaomi MiMo: https://mimo.mi.com/docs/zh-CN/price/pay-as-you-go
-- Default: provider public pay-as-you-go prices. Exception: minimax-m3-openai
-- uses Coding Plan internal preferential pricing at 1/10 (2026-09-23).
-- Review provider billing pages before production import; upstream prices can change.
-- Each provider section records its own review timestamp. Prices can change upstream.

INSERT OR REPLACE INTO model_prices (id, protocol, public_model, input_cost_micro_per_1m, input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m, currency, created_at, updated_at)
VALUES
  -- DeepSeek via Transit Hub. Static cost estimation uses the official Beijing
  -- high-peak tariff (9:00-12:00 and 14:00-18:00), reviewed 2026-08-18.
  ('price_seed_001', 'openai', 'deepseek-v4-flash',          3000000,  100000,   9000000, 'CNY', '2026-08-18T00:00:00Z', '2026-08-18T00:00:00Z'),
  ('price_seed_002', 'openai', 'deepseek-v4-pro',            9000000,  300000,  27000000, 'CNY', '2026-08-18T00:00:00Z', '2026-08-18T00:00:00Z'),

  -- MiniMax via Transit Hub
  -- Coding Plan 内部优惠政策：仅 minimax-m3-openai 按官方按量价的 1/10 计价。
  -- 对应 th-minimax-m3 / th-minimax-m3-vl；非官方降价，Long/Priority/M2 不适用。
  -- CNY/百万 tokens：输入 2.1 -> 0.21，缓存读取 0.42 -> 0.042，输出 8.4 -> 0.84。
  ('price_seed_005', 'openai', 'minimax-m3-openai',              210000,   42000,    840000, 'CNY', '2026-07-20T00:00:00Z', '2026-09-23T00:00:00Z'),
  ('price_seed_006', 'openai', 'minimax-m3-long-openai',        4200000,  840000,  16800000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_seed_007', 'openai', 'minimax-m3-priority-openai',    3150000,  630000,  12600000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_seed_008', 'openai', 'minimax-m3-long-priority-openai', 6300000,1260000, 25200000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_seed_009', 'openai', 'minimax-m2_7-openai',           2100000,  420000,   8400000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),
  ('price_seed_010', 'openai', 'minimax-m2_7-highspeed-openai', 4200000,  420000,  16800000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),

  -- XiaoMi Mimo via Transit Hub
  ('price_seed_017', 'openai', 'mimo-v2_5-pro',                 3000000,   25000,   6000000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z'),

  -- MiniMax's Anthropic-compatible route exposes the same M2.7-highspeed tariff.
  ('price_seed_018', 'anthropic', 'minimax-m2_7-highspeed-anthropic',
                                                               4200000,  420000,  16800000, 'CNY', '2026-07-20T00:00:00Z', '2026-07-20T00:00:00Z');
