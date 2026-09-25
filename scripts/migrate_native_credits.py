#!/usr/bin/env python3
"""Offline, backed-up conversion to native Credits. Default is read-only preflight.

Stop all writers first. Pass every existing control/usage/telemetry database via
--db (including a legacy combined control database). Missing paths are errors.
The only legacy conversion is here; runtime never converts currencies.
"""
import argparse
from contextlib import ExitStack, closing
from datetime import datetime, timezone
import json
from pathlib import Path
import sqlite3
import subprocess

MARKER = 'native_credits'
MAX_INT = 2**63 - 1
AMOUNTS = {
    'cost_quota_micro': 'quota_microcredits',
    'used_cost_micro': 'used_microcredits',
    'cost_remaining_micro': 'remaining_microcredits',
    'cost_micro': 'charged_microcredits',
    'total_cost_micro': 'total_microcredits',
    'input_cost_micro_per_1m': 'input_microcredits_per_1m',
    'input_cache_hit_cost_micro_per_1m': 'input_cache_hit_microcredits_per_1m',
    'output_cost_micro_per_1m': 'output_microcredits_per_1m',
    'input_cost_micro_per_1m_tokens': 'input_microcredits_per_1m_tokens',
    'input_cache_hit_cost_micro_per_1m_tokens': 'input_cache_hit_microcredits_per_1m_tokens',
    'output_cost_micro_per_1m_tokens': 'output_microcredits_per_1m_tokens',
    'cache_write_cost_micro_per_1m_tokens': 'cache_write_microcredits_per_1m_tokens',
}

def ident(name):
    return '"' + name.replace('"', '""') + '"'

def scaled(value):
    if value is None:
        return None
    if isinstance(value, str):
        if not value.lstrip('-').isdigit():
            raise ValueError('Invalid legacy amount')
        value = int(value)
    if type(value) is not int or abs(value) > MAX_INT // 100:
        raise ValueError('Legacy amount is not an integer or overflows micro-Credits')
    return value * 100

def convert_json(value):
    if isinstance(value, list):
        return [convert_json(item) for item in value]
    if not isinstance(value, dict):
        return value
    result = {}
    for key, item in value.items():
        if key in AMOUNTS:
            target = AMOUNTS[key]
            if target in value:
                raise ValueError('Mixed billing units in JSON')
            amount = scaled(item)
            result[target] = None if amount is None else str(amount)
        elif key == 'currency':
            if item != 'CNY':
                raise ValueError('Only explicitly CNY legacy prices can be migrated')
            result['unit'] = 'CREDITS'
        elif 'cost_micro' in key or 'microcredits' in key:
            raise ValueError('Unknown or mixed amount field: ' + key)
        elif key == 'cost_unlimited':
            result['credits_unlimited'] = item
        else:
            result[key] = convert_json(item)
    return result

def convert(db):
    tables = [row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")]
    if 'schema_migrations' in tables and db.execute('SELECT 1 FROM schema_migrations WHERE name=?', (MARKER,)).fetchone():
        return {'already_applied': True}
    counts = {}
    for table in tables:
        columns = [row[1] for row in db.execute('PRAGMA table_info(' + ident(table) + ')')]
        if any('microcredits' in col for col in columns):
            raise ValueError('Native columns without migration marker: ' + table)
        for col in columns:
            if 'cost_micro' in col and col not in AMOUNTS:
                raise ValueError('Unknown legacy monetary column: ' + table + '.' + col)
        if table == 'model_prices':
            if 'currency' not in columns:
                raise ValueError('Model prices have no known currency')
            if db.execute("SELECT 1 FROM model_prices WHERE currency IS NULL OR currency != 'CNY' LIMIT 1").fetchone():
                raise ValueError('Non-CNY model prices require explicit manual review')
        for col in ('rate_limits', 'billing', 'price_snapshot'):
            if col not in columns:
                continue
            rows = db.execute('SELECT rowid, ' + ident(col) + ' FROM ' + ident(table)).fetchall()
            for rowid, raw in rows:
                if raw is None:
                    continue
                obj = json.loads(raw)
                if col == 'price_snapshot' and obj is not None and (not isinstance(obj, dict) or obj.get('currency') != 'CNY'):
                    raise ValueError('Unknown historical price snapshot unit')
                encoded = json.dumps(convert_json(obj), separators=(',', ':'), ensure_ascii=False)
                db.execute('UPDATE ' + ident(table) + ' SET ' + ident(col) + '=? WHERE rowid=?', (encoded, rowid))
        for old, new in AMOUNTS.items():
            if old not in columns:
                continue
            for rowid, amount in db.execute('SELECT rowid, ' + ident(old) + ' FROM ' + ident(table)).fetchall():
                converted = scaled(amount)
                db.execute('UPDATE ' + ident(table) + ' SET ' + ident(old) + '=? WHERE rowid=?', (converted, rowid))
            db.execute('ALTER TABLE ' + ident(table) + ' RENAME COLUMN ' + ident(old) + ' TO ' + ident(new))
        if table == 'model_prices':
            db.execute('ALTER TABLE model_prices DROP COLUMN currency')
            db.execute("ALTER TABLE model_prices ADD COLUMN unit TEXT NOT NULL DEFAULT 'CREDITS' CHECK(unit = 'CREDITS')")
        counts[table] = db.execute('SELECT COUNT(*) FROM ' + ident(table)).fetchone()[0]
    db.execute('CREATE TABLE IF NOT EXISTS schema_migrations(name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)')
    db.execute('INSERT INTO schema_migrations VALUES (?, ?)', (MARKER, datetime.now(timezone.utc).isoformat()))
    if db.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
        raise ValueError('Database integrity check failed')
    return {'already_applied': False, 'rows': counts}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--db', action='append', required=True)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--backup-dir', type=Path)
    args = parser.parse_args()
    paths = list(dict.fromkeys(Path(p).resolve(strict=True) for p in args.db))
    # Read-only dry run against copies, including overflow/JSON/integrity checks.
    reports = []
    for path in paths:
        with closing(sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)) as source, closing(sqlite3.connect(':memory:')) as copy:
            source.backup(copy)
            reports.append({'path': str(path), **convert(copy)})
    if not args.apply or all(r['already_applied'] for r in reports):
        print(json.dumps({'applied': False, 'databases': reports}, indent=2))
        return
    # Offline is mandatory: refuse live processes even though SQLite could lock.
    surfaces = [str(p) + suffix for p in paths for suffix in ('', '-wal', '-shm') if Path(str(p) + suffix).exists()]
    opened = subprocess.run(['lsof', '-t', '--', *surfaces], capture_output=True, text=True)
    if opened.returncode not in (0, 1) or opened.stdout.strip():
        raise SystemExit('Database is open by another process; stop writers before applying migration')
    backup = args.backup_dir or paths[0].parent.parent / 'backups' / ('native-credits-' + datetime.now().strftime('%Y%m%d-%H%M%S'))
    backup.mkdir(parents=True, exist_ok=False)
    with ExitStack() as stack:
        connections = [stack.enter_context(closing(sqlite3.connect(p))) for p in paths]
        try:
            for db in connections:
                db.execute('BEGIN IMMEDIATE')
            # Reserved write locks prevent changes while consistent backups are made.
            for i, path in enumerate(paths):
                with closing(sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)) as source, closing(sqlite3.connect(backup / (str(i) + '-' + path.name))) as target:
                    source.backup(target)
                    target.execute("PRAGMA journal_mode=DELETE")
            reports = [{'path': str(p), **convert(db)} for p, db in zip(paths, connections)]
            (backup / 'manifest.json').write_text(json.dumps({'databases': reports}, indent=2))
            for db in connections:
                db.commit()
        except BaseException:
            for db in connections:
                db.rollback()
            raise
    print(json.dumps({'applied': True, 'backup': str(backup), 'databases': reports}, indent=2))

if __name__ == '__main__':
    main()
