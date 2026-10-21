#!/usr/bin/env python3
"""Rename public TTS models without changing prices, identities or usage.

Default: read-only preview. Stop the backend before --apply on a live server.
Provider YAML and client modelId values must use the new names as well.
Historical request logs, usage buckets and price snapshots are intentionally kept.
"""
import argparse
import json
from pathlib import Path
import sqlite3

RENAMES = {
    "th-dashscope-qwen3-tts-flash": "qwen3-tts-flash",
    "th-minimax-speech-2_8-hd": "minimax-speech-2.8-hd",
}
PROTOCOLS = {"th-dashscope-qwen3-tts-flash": "dashscope_tts", "th-minimax-speech-2_8-hd": "minimax_tts"}


def migrate(db, apply=False):
    tables = {r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    operations = []
    counts = {}

    def add(table, sql, args):
        operations.append((sql, args))
        counts[table] = counts.get(table, 0) + 1

    # Check all uniqueness conflicts before writing anything.
    for table in ["model_prices", "model_states", "route_overrides"]:
        if table not in tables:
            continue
        for old, new in RENAMES.items():
            scope = "public_model=?" if table == "route_overrides" else "protocol=? AND public_model=?"
            old_args = (old,) if table == "route_overrides" else (PROTOCOLS[old], old)
            new_args = (new,) if table == "route_overrides" else (PROTOCOLS[old], new)
            if db.execute("SELECT 1 FROM " + table + " WHERE " + scope, old_args).fetchone():
                if db.execute("SELECT 1 FROM " + table + " WHERE " + scope, new_args).fetchone():
                    raise ValueError("Both old and new model names exist in " + table + "; resolve the conflict first")
                add(table, "UPDATE " + table + " SET public_model=? WHERE " + scope, (new,) + old_args)

    def rename_list(values):
        result = []
        for value in values:
            mapped = RENAMES.get(value, value)
            if mapped not in result:
                result.append(mapped)
        return result

    for table in ["api_keys", "jwt_grants"]:
        if table not in tables:
            continue
        id_column = "id" if table == "api_keys" else "jti"
        for identifier, raw in db.execute("SELECT " + id_column + ",allowed_models FROM " + table):
            values = json.loads(raw)
            if not any(value in RENAMES for value in values):
                continue
            add(table, "UPDATE " + table + " SET allowed_models=? WHERE " + id_column + "=?", (json.dumps(rename_list(values), ensure_ascii=False), identifier))

    if "employee_default_policy" in tables:
        for identifier, raw in db.execute("SELECT id,policy FROM employee_default_policy"):
            policy = json.loads(raw)
            if not any(value in RENAMES for value in policy["allowed_models"]):
                continue
            policy["allowed_models"] = rename_list(policy["allowed_models"])
            add("employee_default_policy", "UPDATE employee_default_policy SET policy=? WHERE id=?", (json.dumps(policy, ensure_ascii=False), identifier))

    if apply:
        for sql, args in operations:
            db.execute(sql, args)
    return counts


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--backup-dir", type=Path)
    args = parser.parse_args()
    path = args.db.resolve(strict=True)
    with sqlite3.connect("file:" + str(path) + ("?mode=rw" if args.apply else "?mode=ro"), uri=True, timeout=30) as db:
        if not args.apply:
            print(json.dumps({"dry_run": True, "changes": migrate(db)}, ensure_ascii=False))
            return
        if args.backup_dir is None:
            parser.error("--backup-dir is required with --apply")
        args.backup_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        target = args.backup_dir / "control.before.db"
        if target.exists():
            raise FileExistsError("Backup already exists; choose a fresh backup directory")
        with sqlite3.connect(target) as saved:
            db.backup(saved)
        db.execute("BEGIN IMMEDIATE")
        changed = migrate(db, apply=True)
        db.commit()
        assert db.execute("PRAGMA quick_check").fetchone()[0] == "ok"
        print(json.dumps({"applied": True, "changes": changed, "backup": str(target)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
