from contextlib import closing
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import sqlite3
import stat
import subprocess
import sys
import tempfile
import unittest


EXPORTER = Path(__file__).with_name("export-x-snapshot.py")


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.output = self.directory / "x-snapshot.json"
        self.now = datetime.now(timezone.utc).replace(microsecond=0)

    def date(self, hours):
        return (self.now + timedelta(hours=hours)).isoformat().replace("+00:00", "Z")

    def run_export(self, *args, data=None):
        return subprocess.run([sys.executable, str(EXPORTER), "--output", str(self.output), *map(str, args)],
                              input=None if data is None else json.dumps(data), text=True, capture_output=True,
                              timeout=10)

    def test_bird_exports_only_recent_verified_public_ids(self):
        def post(ident, handle, date, *, protected=False):
            return {"id": ident, "createdAt": date, "author": {"username": handle, "protected": protected},
                    "text": "content and possible secrets must not leave the adapter"}

        rows = [post(str(value), "DeItaone", self.date(-1)) for value in range(100, 130)]
        rows.extend([
            post("127", "deitaone", self.date(-2)),
            post("999", "OTHER", self.date(-1)),
            post("998", "deitaone", self.date(-37)),
            post("997", "deitaone", self.date(+1)),
            post("996", "deitaone", self.date(-0.5), protected=True),
            post("995/noncanonical", "DeItaone", self.date(-0.5)),
            post("994", "DeItaone", (self.now - timedelta(minutes=10)).strftime("%a %b %d %H:%M:%S %z %Y")),
        ])
        result = self.run_export("--bird-stdin", "--observed-at", self.date(0), data=rows)
        self.assertEqual(result.returncode, 0, result.stderr)
        raw = self.output.read_text(encoding="utf-8")
        doc = json.loads(raw)
        self.assertEqual(set(doc), {"schema", "observedAt", "posts"})
        self.assertEqual(doc["schema"], "brief-studio/x-snapshot/v1")
        self.assertEqual(doc["observedAt"], self.date(0))
        ids = [post["id"] for post in doc["posts"]]
        self.assertEqual(ids, ["994", *map(str, range(129, 105, -1))])
        self.assertTrue(all(post["account"] == "deitaone" for post in doc["posts"]))
        self.assertTrue(all(set(post) == {"id", "account", "publishedAt"} for post in doc["posts"]))
        self.assertNotIn("secrets", raw + result.stdout + result.stderr)
        if sys.platform != "win32":
            self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o600)

    def test_database_reads_existing_collector_schema_and_health(self):
        database = self.directory / "market-posts.db"
        with closing(sqlite3.connect(database)) as con:
            con.execute("CREATE TABLE posts(id TEXT, account TEXT, timestamp TEXT, digest_batch TEXT, text TEXT)")
            con.executemany("INSERT INTO posts VALUES (?, ?, ?, ?, 'private')", [
                ("100", "@DeItaone", self.date(-1), "x-scraper"),
                ("101", "@Business", self.date(-2), "x-scraper"),
                ("102", "@other", self.date(-1), "x-scraper"),
                ("103", "@deitaone", self.date(-1), "rss"),
                ("fj-123", "@DeItaone", self.date(-1), "x-scraper"),
                ("104", "@deitaone", self.date(-40), "x-scraper"),
            ])
            con.commit()
        health = self.directory / "health.json"
        completed = {"status": "caught_up", "complete": True, "error": None}
        health.write_text(json.dumps({"attempted_at": self.date(0), "accounts": {
            "DeItaone": completed, "business": completed,
        }}), encoding="utf-8")
        inputs = {path: path.read_bytes() for path in (database, health)}

        result = self.run_export("--database", database, "--health", health, "--accounts", "@DeItaone, Business")
        self.assertEqual(result.returncode, 0, result.stderr)
        payload = json.loads(self.output.read_text(encoding="utf-8"))
        self.assertEqual(payload["posts"], [
            {"id": "100", "account": "deitaone", "publishedAt": self.date(-1)},
            {"id": "101", "account": "business", "publishedAt": self.date(-2)},
        ])
        for path, content in inputs.items():
            self.assertEqual(content, path.read_bytes())

    def test_failed_or_stale_collection_does_not_overwrite_snapshot(self):
        health = self.directory / "health.json"
        database = self.directory / "must-not-be-opened.db"
        result = self.run_export("--bird-stdin", data=[])
        self.assertEqual(result.returncode, 0, result.stderr)
        original = self.output.read_bytes()
        for attempted_at, status, error in [
            (self.date(-37), "caught_up", None), (self.date(0), "gap_unresolved", "private diagnostic"),
            (self.date(+1), "caught_up", None), (self.date(0), "caught_up", "private diagnostic"),
        ]:
            health.write_text(json.dumps({"attempted_at": attempted_at, "accounts": {"deitaone": {
                "status": status, "complete": True, "error": error,
            }}}), encoding="utf-8")
            result = self.run_export("--database", database, "--health", health)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn("private diagnostic", result.stderr)
            self.assertEqual(original, self.output.read_bytes())
            self.assertFalse(database.exists())

    def test_bird_rejects_non_snapshot_destination_and_unknown_account(self):
        self.output.write_text("unrelated", encoding="utf-8")
        result = self.run_export("--bird-stdin", data=[])
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.output.read_text(encoding="utf-8"), "unrelated")
        result = self.run_export("--bird-stdin", data=[{
            "id": "123", "createdAt": self.date(-1), "author": {"username": "another"},
        }])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("vérifiable", result.stderr)


if __name__ == "__main__":
    unittest.main()
