# /// script
# requires-python = ">=3.13"
# dependencies = ["websockets>=13"]
# ///
"""Backfill grid prices from Home Assistant long-term statistics into `tariffs_backfill`.

evcc stores one row per 15-minute slot: `ts` is the slot start in unix seconds (UTC,
multiple of 900), `grid` the import price in currency/kWh. Home Assistant keeps hourly
statistics (time-weighted mean/min/max) of the price sensor long after its raw states are
purged. This tool expands each HA hour to its four slots, but only after checking that HA
and evcc record the same quantity where both exist:

  1. fetch HA hourly statistics via WebSocket `recorder/statistics_during_period`;
  2. compare each HA hourly mean with the mean of evcc's four slots of that hour over the
     overlap; refuse (exit 2) if they disagree beyond --max-mean-diff / --max-abs-diff;
  3. plan `INSERT OR IGNORE INTO tariffs_backfill (ts, grid)` for every slot of a full HA hour
     that `tariffs` has no grid price for; the table (same columns as `tariffs`) is created if
     absent. evcc's own `tariffs` is never written: only the cheap-charge band's reference
     median reads `tariffs_backfill`, and only where `tariffs` has no price, so the savings
     ledger, Results and the earliest-tariff bound keep evcc's own 15-minute record and never
     see hourly means;
  4. --dry-run prints the row count and writes the SQL; without it the SQL is applied
     inside one transaction. Run it against a copy first.

Usage:
  uv run --python 3.13 backfill_tariffs.py --db evcc-copy.db --token-file .env --dry-run
"""

import argparse
import asyncio
import json
import sqlite3
import statistics
import sys
from datetime import datetime, timezone
from pathlib import Path

import websockets

SLOT = 900
HOUR = 3600
TABLE = "tariffs_backfill"
SCHEMA = [
    f"CREATE TABLE IF NOT EXISTS `{TABLE}` (`ts` integer,`grid` real,`feedin` real,`co2` real,`temperature` real);",
    f"CREATE UNIQUE INDEX IF NOT EXISTS `idx_{TABLE}_timestamp` ON `{TABLE}`(`ts`);",
]


def read_token(path: Path, key: str) -> str:
    for line in path.read_text().splitlines():
        name, _, value = line.partition("=")
        if name.strip() == key:
            return value.strip().strip('"').strip("'")
    raise SystemExit(f"{key} not found in {path}")


async def fetch_hours(url: str, token: str, sensor: str, start: str) -> list[dict]:
    async with websockets.connect(url, max_size=None) as ws:
        await ws.recv()
        await ws.send(json.dumps({"type": "auth", "access_token": token}))
        if json.loads(await ws.recv()).get("type") != "auth_ok":
            raise SystemExit("Home Assistant authentication failed")
        await ws.send(json.dumps({
            "id": 1, "type": "recorder/statistics_during_period", "start_time": start,
            "statistic_ids": [sensor], "period": "hour", "types": ["mean", "min", "max"],
        }))
        reply = json.loads(await ws.recv())
        if not reply.get("success"):
            raise SystemExit(f"statistics request failed: {reply}")
        return reply["result"].get(sensor, [])


def evcc_hours(db: sqlite3.Connection) -> dict[int, list[float]]:
    """evcc grid prices grouped by UTC hour start."""
    hours: dict[int, list[float]] = {}
    for ts, grid in db.execute("SELECT ts, grid FROM tariffs WHERE grid IS NOT NULL"):
        hours.setdefault(ts - ts % HOUR, []).append(grid)
    return hours


def validate(ha: dict[int, float], evcc: dict[int, list[float]]) -> dict:
    """HA hourly mean vs the mean of evcc's four slots, over complete evcc hours."""
    diffs = [ha[h] - statistics.fmean(v) for h, v in evcc.items() if len(v) == 4 and h in ha]
    if not diffs:
        raise SystemExit("no overlapping complete hours: nothing validates the backfill")
    absolute = sorted(abs(d) for d in diffs)
    return {
        "hours": len(diffs),
        "mean_diff": statistics.fmean(diffs),
        "mean_abs_diff": statistics.fmean(absolute),
        "p95_abs_diff": absolute[int(0.95 * (len(absolute) - 1))],
        "max_abs_diff": absolute[-1],
    }


def expansion_error(ha: dict[int, float], evcc: dict[int, list[float]]) -> dict:
    """What expanding an hourly mean to four equal slots costs against evcc's own slots."""
    errors = [abs(ha[h] - p) for h, v in evcc.items() if len(v) == 4 and h in ha for p in v]
    return {"slots": len(errors), "mean_abs": statistics.fmean(errors), "max_abs": max(errors)}


def plan(ha: dict[int, float], recorded: set[int]) -> list[tuple[int, float]]:
    """Every slot of an HA hour that evcc has no grid price for; recorded slots win."""
    rows = []
    for hour in sorted(ha):
        for ts in range(hour, hour + HOUR, SLOT):
            if ts not in recorded:
                rows.append((ts, round(ha[hour], 6)))
    return rows


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--db", required=True, type=Path, help="evcc sqlite database (use a copy)")
    p.add_argument("--token-file", required=True, type=Path, help="file with KEY=token lines")
    p.add_argument("--token-key", default="ha_token")
    p.add_argument("--url", default="wss://home.herold.hn/api/websocket")
    p.add_argument("--sensor", default="sensor.kastanienweg_electricity_price")
    p.add_argument("--start", default="2026-01-01T00:00:00Z")
    p.add_argument("--max-mean-diff", type=float, default=0.002, help="currency/kWh, |mean of differences|")
    p.add_argument("--max-abs-diff", type=float, default=0.005, help="currency/kWh, largest hourly difference")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--sql", type=Path, default=Path("tariffs-backfill.sql"))
    args = p.parse_args()

    stats = asyncio.run(fetch_hours(args.url, read_token(args.token_file, args.token_key), args.sensor, args.start))
    ha = {int(s["start"] // 1000): s["mean"] for s in stats if s.get("mean") is not None and s["end"] - s["start"] == HOUR * 1000}
    if not ha:
        raise SystemExit("no hourly statistics returned")

    db = sqlite3.connect(args.db)
    first = db.execute("SELECT MIN(ts) FROM tariffs").fetchone()[0]
    if first is None:
        raise SystemExit("tariffs is empty: nothing to validate against")
    check = validate(ha, evcc_hours(db))
    report = {
        "ha_first_hour": datetime.fromtimestamp(min(ha), timezone.utc).isoformat(),
        "evcc_first_slot": datetime.fromtimestamp(first, timezone.utc).isoformat(),
        "validation": check,
        "expansion_error": expansion_error(ha, evcc_hours(db)),
    }
    if abs(check["mean_diff"]) > args.max_mean_diff or check["max_abs_diff"] > args.max_abs_diff:
        report["verdict"] = "REFUSED: Home Assistant and evcc do not record the same price"
        print(json.dumps(report, indent=2))
        return 2

    recorded = {ts for (ts,) in db.execute("SELECT ts FROM tariffs WHERE grid IS NOT NULL")}
    rows = plan(ha, recorded)
    report["rows"] = len(rows)
    report["inside_recorded_period"] = sum(1 for ts, _ in rows if ts >= first)
    if rows:
        report["from"] = datetime.fromtimestamp(rows[0][0], timezone.utc).isoformat()
        report["to"] = datetime.fromtimestamp(rows[-1][0] + SLOT, timezone.utc).isoformat()
    statements = ["BEGIN;", *SCHEMA] + [f"INSERT OR IGNORE INTO {TABLE} (ts, grid) VALUES ({ts}, {grid!r});" for ts, grid in rows] + ["COMMIT;"]
    args.sql.write_text("\n".join(statements) + "\n")
    report["sql"] = str(args.sql)
    if args.dry_run:
        report["verdict"] = "dry run: nothing written"
    else:
        db.executescript("\n".join(statements))
        report["verdict"] = f"applied {db.total_changes} rows"
    print(json.dumps(report, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
