-- Static native Credits tariff, based on the supplied zenmind-env registries.
-- Same scale as existing Babelark tariffs: 1 CNY in the source = 100 Credits.
-- Integer micro-Credits per million tokens (1 Credit = 1000000).
-- Cache writes fall back to the input tariff; cache write premiums are not supplied.
BEGIN IMMEDIATE;
INSERT INTO model_prices (
  id, protocol, public_model, input_microcredits_per_1m,
  input_cache_hit_microcredits_per_1m, output_microcredits_per_1m,
  unit, billing, created_at, updated_at
) VALUES
  (
    'price_babelark_claude_haiku_5_5', 'anthropic', 'claude-haiku-5_5',
    70000000, 7000000, 350000000, 'CREDITS',
    '{"mode":"tokens","token_tiers":[{"above_input_tokens":100000,"input_microcredits_per_1m_tokens":"350000000","input_cache_hit_microcredits_per_1m_tokens":"35000000","output_microcredits_per_1m_tokens":"1750000000"}]}',
    strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  ),
  (
    'price_babelark_claude_opus_5_5', 'anthropic', 'claude-opus-5_5',
    2800000000, 140000000, 14000000000, 'CREDITS', '{"mode":"tokens"}',
    strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  ),
  (
    'price_babelark_claude_sonnet_5_5', 'anthropic', 'claude-sonnet-5_5',
    1400000000, 70000000, 7000000000, 'CREDITS', '{"mode":"tokens"}',
    strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  )
ON CONFLICT(protocol, public_model) DO UPDATE SET
  input_microcredits_per_1m = excluded.input_microcredits_per_1m,
  input_cache_hit_microcredits_per_1m = excluded.input_cache_hit_microcredits_per_1m,
  output_microcredits_per_1m = excluded.output_microcredits_per_1m,
  unit = excluded.unit,
  billing = excluded.billing,
  updated_at = excluded.updated_at;
COMMIT;
