-- Babelark official catalog checked 2026-09-23: https://babelark.ai/api/pricing
-- https://babelark.ai/models/gpt-6-luna and https://babelark.ai/models/gpt-6-sol
-- Default group, fixed USD/CNY=7. Requires token_tiers support.
BEGIN IMMEDIATE;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_babelark_gpt-6-luna','openai','gpt-6-luna',700000,70000,3500000,'CNY','{"mode":"tokens","token_tiers":[{"above_input_tokens":272000,"input_cost_micro_per_1m_tokens":1400000,"input_cache_hit_cost_micro_per_1m_tokens":140000,"output_cost_micro_per_1m_tokens":5250000}]}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_babelark_gpt-6-sol','openai','gpt-6-sol',14000000,1400000,70000000,'CNY','{"mode":"tokens","token_tiers":[{"above_input_tokens":272000,"input_cost_micro_per_1m_tokens":28000000,"input_cache_hit_cost_micro_per_1m_tokens":2800000,"output_cost_micro_per_1m_tokens":105000000}]}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
COMMIT;
