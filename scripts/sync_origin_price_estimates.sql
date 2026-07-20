-- Run with the repository root as the working directory:
--   sqlite3 data/transit-hub.db < scripts/sync_origin_price_estimates.sql
--
-- This performs one atomic refresh of the public-list-price estimate scripts.

BEGIN IMMEDIATE;
.read scripts/seed_prices.sql
.read scripts/seed_bailian_token_plan_prices.sql
.read scripts/seed_babelark_origin_prices.sql
COMMIT;
