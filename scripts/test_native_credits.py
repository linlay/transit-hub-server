import json
import sqlite3
import unittest
from migrate_native_credits import convert, MAX_INT

class MigrationTests(unittest.TestCase):
    def legacy(self):
        db = sqlite3.connect(':memory:')
        db.executescript('''
        CREATE TABLE model_prices(input_cost_micro_per_1m INTEGER, currency TEXT, billing TEXT);
        INSERT INTO model_prices VALUES(210000,'CNY','{"mode":"tokens","cache_write_cost_micro_per_1m_tokens":null,"token_tiers":[{"above_input_tokens":272000,"input_cost_micro_per_1m_tokens":420000}]}');
        CREATE TABLE api_keys(cost_quota_micro INTEGER, used_cost_micro INTEGER, rate_limits TEXT);
        INSERT INTO api_keys VALUES(1000000,1200000,'[{"window":"5h","cost_quota_micro":5000}]');
        CREATE TABLE usage_totals(used_cost_micro INTEGER);
        INSERT INTO usage_totals VALUES(1200000);
        CREATE TABLE usage_buckets(cost_micro INTEGER,window_start TEXT);
        INSERT INTO usage_buckets VALUES(123,'unchanged');
        CREATE TABLE request_logs(cost_micro INTEGER,price_snapshot TEXT);
        INSERT INTO request_logs VALUES(123,'{"currency":"CNY","input_cost_micro_per_1m_tokens":210000,"billing":{"mode":"image","image_prices":[{"cost_micro":1000}]}}');
        ''')
        return db

    def test_preserves_visible_balances_and_nested_prices(self):
        with self.legacy() as db:
            convert(db)
            self.assertEqual(db.execute('SELECT input_microcredits_per_1m,unit FROM model_prices').fetchone(),(21000000,'CREDITS'))
            quota, used, limits = db.execute('SELECT * FROM api_keys').fetchone()
            self.assertEqual((quota-used)/1000000,-20)
            self.assertEqual(json.loads(limits)[0]['quota_microcredits'],'500000')
            self.assertEqual(db.execute('SELECT * FROM usage_buckets').fetchone(),(12300,'unchanged'))
            snapshot=json.loads(db.execute('SELECT price_snapshot FROM request_logs').fetchone()[0])
            self.assertEqual(snapshot['billing']['image_prices'][0]['charged_microcredits'],'100000')
            billing=json.loads(db.execute('SELECT billing FROM model_prices').fetchone()[0])
            self.assertIsNone(billing['cache_write_microcredits_per_1m_tokens'])
            self.assertEqual(billing['token_tiers'][0]['above_input_tokens'],272000)
            self.assertEqual(billing['token_tiers'][0]['input_microcredits_per_1m_tokens'],'42000000')
            self.assertTrue(convert(db)['already_applied'])
            self.assertEqual(db.execute('SELECT used_microcredits FROM usage_totals').fetchone()[0],120000000)

    def test_rejects_foreign_currency_and_overflow(self):
        for sql in ["UPDATE model_prices SET currency='USD'",f'UPDATE api_keys SET cost_quota_micro={MAX_INT}']:
            with self.legacy() as db:
                db.execute(sql)
                with self.assertRaises(ValueError):convert(db)

    def test_invalid_snapshot_and_mixed_units(self):
        with self.legacy() as db:
            db.execute("UPDATE request_logs SET price_snapshot='{}'")
            with self.assertRaises(ValueError):convert(db)
        with self.legacy() as db:
            db.execute('ALTER TABLE api_keys ADD COLUMN quota_microcredits INTEGER')
            with self.assertRaises(ValueError):convert(db)


class SeedTests(unittest.TestCase):
    def test_all_seed_scripts_use_native_credits(self):
        from pathlib import Path
        with sqlite3.connect(':memory:') as db:
            db.execute('''CREATE TABLE model_prices(
            id TEXT PRIMARY KEY, protocol TEXT, public_model TEXT,
            input_microcredits_per_1m INTEGER, input_cache_hit_microcredits_per_1m INTEGER,
            output_microcredits_per_1m INTEGER, unit TEXT CHECK(unit='CREDITS'),
            billing TEXT DEFAULT '{"mode":"tokens"}',created_at TEXT,updated_at TEXT,
            UNIQUE(protocol,public_model))''')
            for path in sorted(Path(__file__).parent.glob('seed*.sql')):
                db.executescript(path.read_text())
            self.assertEqual(db.execute('SELECT DISTINCT unit FROM model_prices').fetchall(), [('CREDITS',)])
            self.assertEqual(db.execute("SELECT input_microcredits_per_1m FROM model_prices WHERE public_model='minimax-m3-openai'").fetchone()[0],21000000)
            for (raw,) in db.execute('SELECT billing FROM model_prices'):
                def check(obj):
                    if isinstance(obj,dict):
                        for key,value in obj.items():
                            if 'microcredits' in key:
                                self.assertTrue(value is None or isinstance(value,str))
                            check(value)
                    elif isinstance(obj,list):
                        for item in obj:check(item)
                check(json.loads(raw))

if __name__ == '__main__':unittest.main()
