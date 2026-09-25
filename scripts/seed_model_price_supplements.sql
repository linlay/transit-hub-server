-- Native Credits tariff. Integer micro-Credits per million tokens (1 Credit = 1000000).
-- Preserves the visible Credits prices at the native billing migration; no runtime exchange rate.
BEGIN IMMEDIATE;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-5_6-luna','openai','gpt-5_6-luna',140000000, 14000000, 840000000, 'CREDITS','{"mode":"tokens","token_tiers":[{"above_input_tokens":272000,"input_microcredits_per_1m_tokens":"280000000","output_microcredits_per_1m_tokens":"1260000000","input_cache_hit_microcredits_per_1m_tokens":"28000000"}]}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-5_6-terra','openai','gpt-5_6-terra',1400000000, 140000000, 8400000000, 'CREDITS','{"mode":"tokens","token_tiers":[{"above_input_tokens":272000,"input_microcredits_per_1m_tokens":"2800000000","output_microcredits_per_1m_tokens":"12600000000","input_cache_hit_microcredits_per_1m_tokens":"280000000"}]}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-image-2_5-flare','openai','gpt-image-2_5-flare',3500000000, 875000000, 21000000000, 'CREDITS','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-image-2_5-sunburst','openai','gpt-image-2_5-sunburst',3500000000, 875000000, 21000000000, 'CREDITS','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_gemini-3_1-flash-lite-image','openai','gemini-3_1-flash-lite-image',175000000, 17500000, 21000000000, 'CREDITS','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_gemini-3_1-flash-image-preview','openai','gemini-3_1-flash-image-preview',0, NULL, 0, 'CREDITS','{"mode":"image","image_prices":[{"size":"512x512","quality":"","charged_microcredits":"31500000"},{"size":"1024x1024","quality":"","charged_microcredits":"46900000"},{"size":"2048x2048","quality":"","charged_microcredits":"70700000"},{"size":"4096x4096","quality":"","charged_microcredits":"105700000"}]}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_mimo-v2_6-pro','openai','mimo-v2_6-pro',300000000, 2500000, 600000000, 'CREDITS','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_mimo-v2_6-flash','openai','mimo-v2_6-flash',100000000, 2000000, 200000000, 'CREDITS','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
INSERT INTO model_prices (id,protocol,public_model,input_microcredits_per_1m,input_cache_hit_microcredits_per_1m,output_microcredits_per_1m,unit,billing,created_at,updated_at)
VALUES ('price_supplement_gpt-image-2','openai','gpt-image-2',3500000000, 875000000, 21000000000, 'CREDITS','{"mode":"tokens"}',strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'))
ON CONFLICT(protocol,public_model) DO UPDATE SET
 input_microcredits_per_1m=excluded.input_microcredits_per_1m,
 input_cache_hit_microcredits_per_1m=excluded.input_cache_hit_microcredits_per_1m,
 output_microcredits_per_1m=excluded.output_microcredits_per_1m,
 unit=excluded.unit,billing=excluded.billing,updated_at=excluded.updated_at;
COMMIT;
