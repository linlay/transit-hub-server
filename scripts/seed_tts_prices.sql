-- Static tariff from zenmind-env/registries/models (2026-10-21):
-- dashscope-qwen3-tts-flash.yml: CNY 0.8 / 10,000 characters.
-- minimax-speech-2_8-hd.yml: CNY 3.5 / 10,000 characters.
-- Same scale as existing tariffs: source CNY 1 = 100 Credits.
-- 1 Credit = 1,000,000 micro-Credits; no runtime currency conversion.
BEGIN IMMEDIATE;
INSERT INTO model_prices (
  id, protocol, public_model, input_microcredits_per_1m,
  input_cache_hit_microcredits_per_1m, output_microcredits_per_1m,
  unit, billing, created_at, updated_at
) VALUES
  (
    'price_dashscope_qwen3_tts_flash', 'dashscope_tts', 'qwen3-tts-flash',
    0, NULL, 0, 'CREDITS',
    '{"mode":"characters","microcredits_per_10k_characters":"80000000"}',
    strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  ),
  (
    'price_minimax_speech_2_8_hd', 'minimax_tts', 'minimax-speech-2.8-hd',
    0, NULL, 0, 'CREDITS',
    '{"mode":"characters","microcredits_per_10k_characters":"350000000"}',
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
