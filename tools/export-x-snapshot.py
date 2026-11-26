#!/usr/bin/env python3
"""Export recent X references from a local collector or a captured Bird response."""

import argparse
from contextlib import closing
from datetime import datetime, timedelta, timezone
from email.utils import parsedate_to_datetime
import json
import os
from pathlib import Path
import re
import sqlite3
import sys
import tempfile


SCHEMA = "brief-studio/x-snapshot/v1"
MAX_AGE = timedelta(hours=36)
MAX_POSTS = 25
HANDLE = re.compile(r"[a-zA-Z0-9_]{1,15}", re.ASCII)
POST_ID = re.compile(r"[1-9][0-9]{0,18}", re.ASCII)


class SnapshotError(Exception):
    """An error whose text is safe to print."""


class Arguments(argparse.ArgumentParser):
    def error(self, message):
        raise SnapshotError("Options invalides. Utilise --help pour voir les paramètres attendus.")


def parse_args(argv):
    parser = Arguments(description="Exporter les références X récentes pour Brief Studio.")
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--database", type=Path, help="base SQLite du collecteur X existant")
    source.add_argument("--bird-stdin", action="store_true", help="lire une capture Bird JSON sur stdin")
    parser.add_argument("--health", type=Path, help="fichier health.json du collecteur SQLite")
    parser.add_argument("--observed-at", help="heure réelle de la capture Bird au format RFC 3339")
    parser.add_argument("--accounts", default="deitaone", help="un à cinq comptes X séparés par des virgules")
    parser.add_argument("--output", required=True, type=Path, help="chemin du fichier JSON à produire")
    args = parser.parse_args(argv)
    if args.bird_stdin and args.health is not None:
        parser.error("--health requires --database")
    if args.database is not None and (args.health is None or args.observed_at is not None):
        parser.error("--database requires --health and cannot take --observed-at")
    return args


def normalize_handle(value):
    if not isinstance(value, str):
        return None
    account = value.strip().removeprefix("@")
    return account.lower() if HANDLE.fullmatch(account) else None


def configured_accounts(value):
    handles = [normalize_handle(part) for part in value.split(",")]
    if len(handles) > 5 or None in handles:
        raise SnapshotError("Saisis de un à cinq comptes X séparés par des virgules.")
    return tuple(dict.fromkeys(handles))


def parse_date(value, *, bird=False):
    if not isinstance(value, str) or len(value) > 60:
        return None
    try:
        stamp = datetime.fromisoformat(value[:-1] + "+00:00" if value.endswith("Z") else value)
    except ValueError:
        if not bird:
            return None
        try:
            stamp = parsedate_to_datetime(value)
        except (TypeError, ValueError, OverflowError):
            return None
    try:
        return stamp.astimezone(timezone.utc) if stamp.tzinfo is not None else None
    except (ValueError, OverflowError):
        return None


def format_date(value):
    return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def read_json(stream, limit, reason):
    try:
        body = stream.read(limit + 1)
        if len(body) <= limit:
            return json.loads(body)
    except (OSError, ValueError, RecursionError):
        pass
    raise SnapshotError(reason)


def verify_observed_at(observed, now):
    if observed is None or observed > now:
        raise SnapshotError("L’heure réelle de la collecte X est manquante ou incohérente.")
    if observed < now - MAX_AGE:
        raise SnapshotError("La dernière collecte X a plus de 36 heures. Actualise la source avant l’export.")


def collector_observed_at(path, accounts, now):
    with path.open("rb") as stream:
        health = read_json(stream, 64 << 10, "L’état du collecteur X est illisible.")
    if not isinstance(health, dict) or not isinstance(health.get("accounts"), dict):
        raise SnapshotError("L’état du collecteur X est incomplet.")
    records = {normalize_handle(key): value for key, value in health["accounts"].items()}
    for handle in accounts:
        status = records.get(handle)
        if (not isinstance(status, dict) or status.get("complete") is not True or
                status.get("error") is not None or status.get("status") not in ("caught_up", "initial_window")):
            raise SnapshotError("La dernière collecte X n’est pas complète pour tous les comptes demandés.")
    observed = parse_date(health.get("attempted_at"))
    verify_observed_at(observed, now)
    return observed


def database_posts(path, accounts, earliest, observed):
    placeholders = ", ".join("?" for _ in accounts)
    query = f"""
        SELECT id, account, timestamp
        FROM posts
        WHERE digest_batch = 'x-scraper' AND lower(account) IN ({placeholders})
          AND length(id) BETWEEN 1 AND 19 AND id GLOB '[1-9]*' AND id NOT GLOB '*[^0-9]*'
          AND julianday(timestamp) >= julianday(?) AND julianday(timestamp) <= julianday(?)
        ORDER BY julianday(timestamp) DESC, length(id) DESC, id DESC
        LIMIT ?
    """
    uri = path.resolve(strict=True).as_uri() + "?mode=ro"
    params = (*("@" + account for account in accounts), format_date(earliest), format_date(observed), MAX_POSTS)
    with closing(sqlite3.connect(uri, uri=True, timeout=2)) as database:
        rows = database.execute(query, params).fetchall()
    return [(ident, normalize_handle(account), parse_date(stamp)) for ident, account, stamp in rows]


def bird_posts(rows, accounts):
    if not isinstance(rows, list):
        raise SnapshotError("La capture Bird doit être un tableau JSON de publications.")
    posts = []
    for row in rows:
        if not isinstance(row, dict) or not isinstance(row.get("author"), dict):
            continue
        author = row["author"]
        if author.get("protected") is True:
            continue
        account = normalize_handle(author.get("username"))
        ident = row.get("id")
        published = parse_date(row.get("createdAt"), bird=True)
        if account in accounts and isinstance(ident, str) and POST_ID.fullmatch(ident) and published is not None:
            posts.append((ident, account, published))
    if rows and not posts:
        raise SnapshotError("La capture Bird ne contient aucune publication vérifiable des comptes demandés.")
    return posts


def snapshot(posts, accounts, earliest, observed):
    entries = {}
    for ident, account, published in posts:
        if account not in accounts or not isinstance(ident, str) or not POST_ID.fullmatch(ident):
            continue
        if published is None or not earliest <= published <= observed:
            continue
        if ident not in entries or published > entries[ident][1]:
            entries[ident] = (account, published)
    ordered = sorted(entries.items(), key=lambda item: (item[1][1], len(item[0]), item[0]), reverse=True)
    return {
        "schema": SCHEMA,
        "observedAt": format_date(observed),
        "posts": [{"id": ident, "account": account, "publishedAt": format_date(published)}
                  for ident, (account, published) in ordered[:MAX_POSTS]],
    }


def write_snapshot(path, payload):
    if path.is_symlink() or path.exists():
        reason = "Le fichier de destination existe déjà et ne constitue pas un instantané X."
        if path.is_symlink() or not path.is_file():
            raise SnapshotError(reason)
        with path.open("rb") as stream:
            previous = read_json(stream, 64 << 10, reason)
        if not isinstance(previous, dict) or previous.get("schema") != SCHEMA:
            raise SnapshotError(reason)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", prefix=".x-snapshot-",
                                         dir=path.parent, delete=False) as stream:
            temporary = Path(stream.name)
            json.dump(payload, stream, ensure_ascii=True, separators=(",", ":"))
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def main(argv=None):
    try:
        args = parse_args(argv)
        accounts = configured_accounts(args.accounts)
        output = args.output.expanduser()
        if args.bird_stdin:
            rows = read_json(sys.stdin.buffer, 1 << 20, "La capture Bird est illisible ou trop volumineuse.")
            now = datetime.now(timezone.utc)
            observed = parse_date(args.observed_at) if args.observed_at is not None else now
            verify_observed_at(observed, now)
            posts = bird_posts(rows, accounts)
        else:
            database = args.database.expanduser()
            health = args.health.expanduser()
            if output.resolve() in (database.resolve(), health.resolve()):
                raise SnapshotError("L’instantané doit être écrit dans un fichier distinct de la base et de son état.")
            now = datetime.now(timezone.utc)
            observed = collector_observed_at(health, accounts, now)
            posts = database_posts(database, accounts, now - MAX_AGE, observed)
        payload = snapshot(posts, accounts, now - MAX_AGE, observed)
        write_snapshot(output, payload)
    except SnapshotError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    except (OSError, sqlite3.Error, RuntimeError):
        print("Impossible de lire les données locales ou d’écrire l’instantané X.", file=sys.stderr)
        return 1
    print(f"Instantané X écrit avec {len(payload['posts'])} publication(s) récente(s).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
