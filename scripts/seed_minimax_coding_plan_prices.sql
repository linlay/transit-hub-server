-- 2026-09-23: Coding Plan 内部优惠政策，非 MiniMax 官方按量降价。
-- 仅 openai/minimax-m3-openai（th-minimax-m3 和 th-minimax-m3-vl 共用）按 1/10 计价。
-- CNY/百万 tokens：输入 2.1 -> 0.21，缓存读取 0.42 -> 0.042，输出 8.4 -> 0.84。
-- 数据库存储单位为 micro-CNY/百万 tokens。Long/Priority/M2 不适用。
-- 使用绝对价格，重复执行不会再次除以 10；保留现有 id、billing 和历史用量。
INSERT INTO model_prices (
  id, protocol, public_model, input_cost_micro_per_1m,
  input_cache_hit_cost_micro_per_1m, output_cost_micro_per_1m,
  currency, created_at, updated_at
) VALUES (
  'price_seed_005', 'openai', 'minimax-m3-openai', 210000,
  42000, 840000, 'CNY', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
  strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
)
ON CONFLICT(protocol, public_model) DO UPDATE SET
  input_cost_micro_per_1m = excluded.input_cost_micro_per_1m,
  input_cache_hit_cost_micro_per_1m = excluded.input_cache_hit_cost_micro_per_1m,
  output_cost_micro_per_1m = excluded.output_cost_micro_per_1m,
  currency = excluded.currency,
  updated_at = excluded.updated_at;
