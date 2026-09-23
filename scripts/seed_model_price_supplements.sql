-- Verified 2026-09-23. See docs/babelark-pricing-2026-09-23.md.
-- Requires current server schema (model_prices.billing). USD/CNY fixed policy = 7.
-- Import ONLY with server supporting token-priced images and token_tiers.
-- User confirmed gpt-image-2 (upstream gpt-image-2-t) uses the Image 2.5 tariff.
BEGIN IMMEDIATE;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-5_6-luna','openai','gpt-5_6-luna',1400000,140000,8400000,'CNY','{"mode":"tokens","token_tiers":[{"above_input_tokens":272000,"input_cost_micro_per_1m_tokens":2800000,"output_cost_micro_per_1m_tokens":12600000,"input_cache_hit_cost_micro_per_1m_tokens":280000}]}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-5_6-terra','openai','gpt-5_6-terra',14000000,1400000,84000000,'CNY','{"mode":"tokens","token_tiers":[{"above_input_tokens":272000,"input_cost_micro_per_1m_tokens":28000000,"output_cost_micro_per_1m_tokens":126000000,"input_cache_hit_cost_micro_per_1m_tokens":2800000}]}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-image-2_5-flare','openai','gpt-image-2_5-flare',35000000,8750000,210000000,'CNY','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-image-2_5-sunburst','openai','gpt-image-2_5-sunburst',35000000,8750000,210000000,'CNY','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_gemini-3_1-flash-lite-image','openai','gemini-3_1-flash-lite-image',1750000,175000,210000000,'CNY','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_gemini-3_1-flash-image-preview','openai','gemini-3_1-flash-image-preview',0,NULL,0,'CNY','{"mode":"image","image_prices":[{"size":"512x512","quality":"","cost_micro":315000},{"size":"1024x1024","quality":"","cost_micro":469000},{"size":"2048x2048","quality":"","cost_micro":707000},{"size":"4096x4096","quality":"","cost_micro":1057000}]}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_mimo-v2_6-pro','openai','mimo-v2_6-pro',3000000,25000,6000000,'CNY','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_mimo-v2_6-flash','openai','mimo-v2_6-flash',1000000,20000,2000000,'CNY','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_cost_micro_per_1m,input_cache_hit_cost_micro_per_1m,output_cost_micro_per_1m,currency,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-image-2','openai','gpt-image-2',35000000,8750000,210000000,'CNY','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_cost_micro_per_1m=excluded.input_cost_micro_per_1m,
 input_cache_hit_cost_micro_per_1m=excluded.input_cache_hit_cost_micro_per_1m,
 output_cost_micro_per_1m=excluded.output_cost_micro_per_1m,
 currency=excluded.currency,billing=excluded.billing,updated_at=excluded.updated_at;
COMMIT;
