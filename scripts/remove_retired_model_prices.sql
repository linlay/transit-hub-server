-- Remove only retired OpenAI model prices; preserve usage and request history.
BEGIN IMMEDIATE;
DELETE FROM model_prices
WHERE protocol = 'openai' AND public_model IN (
  'grok-4.5',
  'deepseek-chat',
  'deepseek-reasoner',
  'mimo-v2_5',
  'minimax-m2-openai',
  'minimax-m2_1-openai',
  'minimax-m2_1-highspeed-openai',
  'minimax-m2_5-openai',
  'minimax-m2_5-highspeed-openai'
);
COMMIT;
