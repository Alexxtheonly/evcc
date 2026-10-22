"""Offline tests for backfill_hp.py: fixtures only, no network.

Run: python3 -m unittest discover -s contrib/hp-backfill
"""

import argparse
import importlib.util
import io
import json
import math
import sqlite3
import tempfile
import unittest
from contextlib import redirect_stdout
from datetime import date, datetime, timedelta, timezone
from pathlib import Path

spec = importlib.util.spec_from_file_location("backfill_hp", Path(__file__).with_name("backfill_hp.py"))
bf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bf)

START = int(datetime(2025, 12, 1, 0, 0, tzinfo=bf.BERLIN).timestamp())
IMPORT, RETURNED = "sensor.import", "sensor.returned"


def make_db(path: Path, hp_rows=(), entity=True) -> None:
    db = sqlite3.connect(path)
    db.executescript(
        'CREATE TABLE entities (id integer primary key, "group" text, name text, title text);'
        "CREATE TABLE meters (meter integer, ts integer, energy real, return_energy real, soc_temp real, recovered numeric, incomplete numeric);"
        "CREATE UNIQUE INDEX meters_meter_ts ON meters(meter, ts);"
    )
    db.execute("INSERT INTO entities VALUES (1, 'home', 'home', '')")
    if entity:
        db.execute("INSERT INTO entities VALUES (19, 'meter', 'db:19', 'Heat pump')")
    db.executemany("INSERT INTO meters (meter, ts, energy, recovered, incomplete) VALUES (19, ?, ?, ?, ?)", hp_rows)
    db.commit()
    db.close()


def hours(n, value=1.0, first=START):
    return {first + i * bf.HOUR: value for i in range(n)}


def args_for(db: Path, **kw) -> argparse.Namespace:
    argv = ["--db", str(db), "--token-file", "unused", "--meter", "db:19"]
    for k, v in kw.items():
        argv += [f"--{k.replace('_', '-')}"] + ([] if v is True else [str(v)])
    return bf.build_parser().parse_args(argv)


def run_tool(db: Path, ha, temp=None, now=None, **kw):
    out = io.StringIO()
    with redirect_stdout(out):
        code = bf.run(args_for(db, **kw), fetch_ha=lambda sensors, start: ha, fetch_temp=temp or (lambda *a: {}), now=now)
    return code, json.loads(out.getvalue())


def table_rows(path: Path, table: str):
    db = sqlite3.connect(path)
    try:
        return db.execute(f"SELECT * FROM {table} ORDER BY 1, 2").fetchall()
    finally:
        db.close()


class ExpansionTest(unittest.TestCase):
    def test_hour_becomes_four_even_slots(self):
        slots = bf.expand(START, 2.0)
        self.assertEqual([ts for ts, _ in slots], [START + k * 900 for k in range(4)])
        self.assertEqual([v for _, v in slots], [0.5] * 4)
        self.assertAlmostEqual(sum(v for _, v in slots), 2.0)

    def test_temperature_repeats_the_hour_value(self):
        rows = bf.plan_temperature({START: -1.5, START + 3600: None, START + 7200: math.nan, START + 10800: 0.0})
        self.assertEqual(rows, [(START + k * 900, -1.5) for k in range(4)] + [(START + 10800 + k * 900, 0.0) for k in range(4)])


class RefusalTest(unittest.TestCase):
    def accept(self, imports, returned, not_before=START):
        return bf.accept_hours(imports, returned, not_before)

    def test_clean_hours_pass(self):
        accepted, refused = self.accept(hours(3, 1.2), hours(3, 0.0))
        self.assertEqual(len(accepted), 3)
        self.assertEqual(sum(refused.values()), 0)

    def test_before_the_bound_is_refused_whatever_it_says(self):
        imports = hours(4, 1.0, first=START - 2 * 3600)
        accepted, refused = self.accept(imports, hours(4, 0.0, first=START - 2 * 3600))
        self.assertEqual(sorted(accepted), [START, START + 3600])
        self.assertEqual(refused["before_bound"], 2)

    def test_returned_energy_over_the_limit(self):
        returned = hours(4, 0.0)
        returned[START + 3600] = 0.0011
        returned[START + 7200] = 0.001  # exactly the limit still counts as none
        returned[START + 10800] = 5.0
        accepted, refused = self.accept(hours(4), returned)
        self.assertEqual(sorted(accepted), [START, START + 7200])
        self.assertEqual(refused["returned_energy"], 2)

    def test_negative_returned_change_is_unverifiable(self):
        returned = hours(3, 0.0)
        returned[START + 3600] = -0.0001
        returned[START + 7200] = -40.0
        accepted, refused = self.accept(hours(3), returned)
        self.assertEqual(list(accepted), [START])
        self.assertEqual(refused["returned_energy"], 2)

    def test_unverifiable_and_invalid_hours(self):
        imports = {START: 1.0, START + 3600: -0.5, START + 7200: math.nan, START + 10800: math.inf, START + 14400: None, START + 18000: 1.0}
        returned = {START: 0.0, START + 3600: 0.0, START + 7200: 0.0, START + 10800: 0.0, START + 14400: 0.0}
        accepted, refused = self.accept(imports, returned)
        self.assertEqual(list(accepted), [START])
        self.assertEqual(refused["invalid_change"], 4)
        self.assertEqual(refused["returned_missing"], 1, "no returned reading: cannot be verified")

    def test_zero_is_a_value(self):
        accepted, _ = self.accept({START: 0.0}, {START: 0.0})
        self.assertEqual(accepted, {START: 0.0})

    def test_bound_cannot_be_lowered(self):
        self.assertEqual(bf.parse_not_before("2025-11-17"), bf.VALID_FROM)
        self.assertEqual(bf.parse_not_before("2025-12-01").hour, 0)
        for early in ("2025-11-16", "2025-11-16T23:00:00+01:00", "2024-01-01"):
            with self.assertRaises(SystemExit):
                bf.parse_not_before(early)
        with self.assertRaises(SystemExit):
            bf.parse_not_before("not a date")

    def test_bound_is_local_midnight(self):
        self.assertEqual(bf.VALID_FROM.utcoffset(), timedelta(hours=1))
        self.assertEqual(bf.VALID_FROM.astimezone(timezone.utc).hour, 23)


class PlanTest(unittest.TestCase):
    def test_real_clean_slots_win(self):
        accepted = {START: 1.0, START + 3600: 2.0}
        rows = bf.plan_energy(accepted, {START + 900, START + 3600})
        self.assertEqual([ts for ts, _ in rows], [START, START + 1800, START + 2700] + [START + 3600 + 900 * k for k in (1, 2, 3)])
        self.assertEqual(dict(rows)[START], 0.25)

    def test_cross_check(self):
        accepted = hours(7 * 24, 1.0)
        real = {ts + k * 900: 0.25 for ts in accepted for k in range(4)}
        ok = bf.cross_check(accepted, real, 7, 0.02, 0.15)
        self.assertEqual(ok["verdict"], "ok")
        self.assertEqual(ok["overlap_hours"], 168)

        skipped = bf.cross_check(accepted, {k: v for k, v in list(real.items())[:100]}, 7, 0.02, 0.15)
        self.assertTrue(skipped["verdict"].startswith("skipped"))

        biased = {ts: v * 1.2 for ts, v in real.items()}
        with self.assertRaises(bf.CrossCheckFailed) as e:
            bf.cross_check(accepted, biased, 7, 0.02, 0.15)
        self.assertGreater(abs(e.exception.report["mean_diff"]), 0.02)

        spike = dict(real)
        spike[START + 900] = 3.0
        with self.assertRaises(bf.CrossCheckFailed):
            bf.cross_check(accepted, spike, 7, 0.02, 0.15)

    def test_incomplete_hours_are_not_compared(self):
        accepted = hours(7 * 24, 1.0)
        real = {ts + k * 900: 0.25 for ts in accepted for k in range(4)}
        del real[START + 900]  # one hour has only three slots: not a complete hour
        report = bf.cross_check(accepted, real, 7, 0.02, 0.15)
        self.assertEqual(report["overlap_hours"], 7 * 24 - 1)

    def test_chunks_are_at_most_90_days_and_cover_the_range(self):
        chunks = bf.temperature_chunks(date(2025, 11, 17), date(2026, 10, 21))
        self.assertEqual(chunks[0][0], date(2025, 11, 17))
        self.assertEqual(chunks[-1][1], date(2026, 10, 21))
        for (a, b), (c, _) in zip(chunks, chunks[1:]):
            self.assertLessEqual((b - a).days + 1, 90)
            self.assertEqual(c, b + timedelta(days=1))
        self.assertEqual(bf.temperature_chunks(date(2025, 11, 17), date(2025, 11, 16)), [])

    def test_open_meteo_payload(self):
        payload = {"hourly": {"time": ["2025-12-01T00:00", "2025-12-01T01:00"], "temperature_2m": [1.5, None]}}
        self.assertEqual(bf.parse_temperature(payload), {int(datetime(2025, 12, 1, tzinfo=timezone.utc).timestamp()): 1.5, int(datetime(2025, 12, 1, 1, tzinfo=timezone.utc).timestamp()): None})


class DatabaseTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.path = Path(self.dir.name) / "evcc.db"

    def ha(self, n=48, value=1.0):
        return {IMPORT: hours(n, value), RETURNED: hours(n, 0.0)}

    def run_with(self, ha, **kw):
        kw.setdefault("import_sensor", IMPORT)
        kw.setdefault("returned_sensor", RETURNED)
        kw.setdefault("not_before", "2025-12-01")
        return run_tool(self.path, ha, **kw)

    def test_unknown_meter_is_an_error(self):
        make_db(self.path, entity=False)
        with self.assertRaises(SystemExit) as e:
            self.run_with(self.ha())
        self.assertIn("db:19", str(e.exception))

    def test_writes_only_the_backfill_table_and_a_rerun_changes_nothing(self):
        make_db(self.path, hp_rows=[(START + 900, 0.1, 0, 0), (START + 1800, 0.0, 0, 1)])
        meters_before = table_rows(self.path, "meters")

        code, first = self.run_with(self.ha(48, 1.0))
        self.assertEqual(code, 0)
        self.assertEqual(first["verdict"], "applied")
        self.assertEqual(first["changed"]["meters_backfill"], 48 * 4 - 1)
        self.assertEqual(first["energy_slots_skipped_real"], 1)

        rows = table_rows(self.path, "meters_backfill")
        self.assertEqual(len(rows), 48 * 4 - 1)
        self.assertTrue(all(r[0] == 19 and r[3] == "ha_hourly" and r[2] == 0.25 for r in rows))
        self.assertNotIn(START + 900, [r[1] for r in rows], "a clean real slot is not backfilled")
        self.assertIn(START + 1800, [r[1] for r in rows], "a flagged real slot is not a measurement")
        self.assertEqual(table_rows(self.path, "meters"), meters_before, "meters is never written")

        snapshot = table_rows(self.path, "meters_backfill")
        code, second = self.run_with(self.ha(48, 1.0))
        self.assertEqual(code, 0)
        self.assertEqual(second["changed"], {"meters_backfill": 0, "temperature_backfill": 0})
        self.assertEqual(table_rows(self.path, "meters_backfill"), snapshot)

        code, third = self.run_with(self.ha(48, 2.0))
        self.assertEqual(third["changed"]["meters_backfill"], 48 * 4 - 1, "a changed value is updated, not duplicated")
        self.assertEqual(len(table_rows(self.path, "meters_backfill")), 48 * 4 - 1)

    def test_no_accepted_hours_exits_3_also_in_dry_run(self):
        make_db(self.path)
        before = self.path.read_bytes()
        for kw in ({}, {"dry_run": True}):
            for ha in ({IMPORT: hours(5), RETURNED: {}}, {IMPORT: {}, RETURNED: hours(5, 0.0)}, {}, {IMPORT: hours(5), RETURNED: hours(5, 1.0)}):
                code, report = self.run_with(ha, **kw)
                self.assertEqual(code, 3)
                self.assertEqual(report["hours_accepted"], 0)
        self.assertEqual(self.path.read_bytes(), before)

    def test_dry_run_writes_nothing_not_even_the_tables(self):
        make_db(self.path)
        before = self.path.read_bytes()
        code, report = self.run_with(self.ha(), dry_run=True)
        self.assertEqual(code, 0)
        self.assertEqual(report["would_change"]["meters_backfill"], 48 * 4)
        self.assertEqual(self.path.read_bytes(), before)

        self.run_with(self.ha())
        code, report = self.run_with(self.ha(), dry_run=True)
        self.assertEqual(report["would_change"], {"meters_backfill": 0, "temperature_backfill": 0})

    def test_refusals_are_counted_and_not_written(self):
        make_db(self.path)
        ha = {IMPORT: hours(10, 1.0, first=START - 4 * 3600), RETURNED: hours(10, 0.0, first=START - 4 * 3600)}
        ha[RETURNED][START + 3600] = 0.5
        code, report = self.run_with(ha)
        self.assertEqual(report["refused"]["before_bound"], 4)
        self.assertEqual(report["refused"]["returned_energy"], 1)
        self.assertEqual(report["hours_accepted"], 5)
        starts = {r[1] for r in table_rows(self.path, "meters_backfill")}
        self.assertTrue(all(ts >= START for ts in starts))
        self.assertNotIn(START + 3600, starts)

    def test_cross_check_failure_exits_2_and_writes_nothing(self):
        real = [(START + k * 900, 0.25 * 3, 0, 0) for k in range(7 * 24 * 4)]  # evcc recorded three times less
        make_db(self.path, hp_rows=real)
        before = self.path.read_bytes()
        code, report = self.run_with(self.ha(7 * 24, 1.0))
        self.assertEqual(code, 2)
        self.assertTrue(report["cross_check"]["verdict"].startswith("REFUSED"))
        self.assertEqual(self.path.read_bytes(), before)

        make_db_ok = Path(self.dir.name) / "ok.db"
        make_db(make_db_ok, hp_rows=[(START + k * 900, 0.25, 0, 0) for k in range(7 * 24 * 4)])
        code, report = run_tool(make_db_ok, self.ha(7 * 24, 1.0), import_sensor=IMPORT, returned_sensor=RETURNED, not_before="2025-12-01")
        self.assertEqual(code, 0)
        self.assertEqual(report["cross_check"]["verdict"], "ok")

    def test_temperature_is_idempotent_and_separate(self):
        make_db(self.path)
        now = datetime(2025, 12, 3, 12, 0, tzinfo=timezone.utc)
        seen = []

        def temp(url, lat, lon, start, end):
            seen.append((url, lat, lon, start, end))
            return {START + i * 3600: -1.0 + i for i in range(24)}

        code, first = run_tool(self.path, self.ha(), temp=temp, now=now, temperature=True, import_sensor=IMPORT, returned_sensor=RETURNED, not_before="2025-12-01")
        self.assertEqual(code, 0)
        self.assertEqual(first["changed"]["temperature_backfill"], 24 * 4)
        self.assertEqual(seen[0][1:3], (49.1579, 9.1737))
        self.assertIn("historical-forecast-api.open-meteo.com", seen[0][0])
        self.assertEqual(seen[0][4], date(2025, 12, 2), "up to yesterday")
        rows = table_rows(self.path, "temperature_backfill")
        self.assertEqual(rows[0], (START, -1.0, "open-meteo"))
        self.assertEqual(rows[3], (START + 2700, -1.0, "open-meteo"))

        code, second = run_tool(self.path, self.ha(), temp=temp, now=now, temperature=True, import_sensor=IMPORT, returned_sensor=RETURNED, not_before="2025-12-01")
        self.assertEqual(second["changed"], {"meters_backfill": 0, "temperature_backfill": 0})

    def test_schema_matches_the_go_tables(self):
        db = sqlite3.connect(":memory:")
        for statement in bf.SCHEMA:
            db.execute(statement)
        self.assertEqual([c[1] for c in db.execute("PRAGMA table_info(meters_backfill)")], ["meter", "ts", "energy", "source"])
        self.assertEqual([c[1] for c in db.execute("PRAGMA table_info(temperature_backfill)")], ["ts", "temperature", "source"])
        indexes = {r[1]: r[2] for r in db.execute("PRAGMA index_list(meters_backfill)")}
        self.assertEqual(indexes, {"meters_backfill_meter_ts": 1}, "unique (meter, ts)")


if __name__ == "__main__":
    unittest.main()
