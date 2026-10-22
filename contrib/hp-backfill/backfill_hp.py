# /// script
# requires-python = ">=3.11"
# dependencies = ["websockets>=13", "tzdata"]
# ///
"""Backfill the heat pump's winter history from Home Assistant into `meters_backfill`.

evcc records the heat pump (a Shelly Pro 3EM added as ext meter) as one `meters` row per
15-minute slot (`ts` slot start in unix seconds, `energy` in kWh) from the day it is added.
Home Assistant keeps hourly statistics of the same Shelly for the winter before. The
heat-pump forecast fits its daily model on both. This tool:

  1. fetches the Shelly's hourly `change` of total active energy and of total active
     *returned* energy via WebSocket `recorder/statistics_during_period`;
  2. refuses every hour before --not-before (default and floor 2025-11-17 00:00
     Europe/Berlin: the Shelly's earlier data is invalid), every hour whose returned energy
     changed by more than 0.001 kWh, is negative (counter reset) or is missing, and every hour with a negative, missing
     or non-finite change; the counts are reported;
  3. cross-checks the accepted hours against evcc's own `meters` rows of the heat pump once
     at least --min-overlap-days days of complete hours overlap: if the hourly sums disagree
     beyond --max-mean-diff / --max-abs-diff, exit 2 and write nothing;
  4. expands each accepted hour evenly to its four slots and upserts them into
     `meters_backfill (meter, ts, energy, source)`, except slots where `meters` already has
     a clean heat-pump row (real data wins). `meters` is never written, and evcc's profile,
     ledger and Results never read this table: only the heat-pump daily fit does;
  5. with --temperature also fetches the hourly outdoor temperature (Open-Meteo
     *historical forecast* API, the same provider as evcc's forecast) into
     `temperature_backfill (ts, temperature, source)`, expanded to four slots. The Home
     Assistant heat-pump sensor is not used: it sat at 1.1 C for days.

Reruns are no-ops: rows are only updated when their value differs, so a second run reports
0 changed rows. --dry-run opens the database read-only and writes nothing.

Usage (run against a copy of evcc.db first):
  uv run backfill_hp.py --db evcc-copy.db --token-file .env --meter db:19 --temperature --dry-run
"""

import argparse
import asyncio
import json
import math
import sqlite3
import statistics
import sys
import urllib.parse
import urllib.request
from datetime import date, datetime, timedelta, timezone
from pathlib import Path
from zoneinfo import ZoneInfo

SLOT = 900
HOUR = 3600
BERLIN = ZoneInfo("Europe/Berlin")
# the Shelly's data before this is invalid (wiring/CT: kWh/day of phantom returned energy)
VALID_FROM = datetime(2025, 11, 17, 0, 0, tzinfo=BERLIN)
RETURNED_LIMIT = 0.001  # kWh per hour of returned energy that still counts as none
SOURCE = "ha_hourly"
TEMPERATURE_SOURCE = "open-meteo"
CHUNK_DAYS = 90

SCHEMA = [
    "CREATE TABLE IF NOT EXISTS `meters_backfill` (`meter` integer,`ts` integer,`energy` real,`source` text);",
    "CREATE UNIQUE INDEX IF NOT EXISTS `meters_backfill_meter_ts` ON `meters_backfill`(`meter`,`ts`);",
    "CREATE TABLE IF NOT EXISTS `temperature_backfill` (`ts` integer,`temperature` real,`source` text);",
    "CREATE UNIQUE INDEX IF NOT EXISTS `temperature_backfill_ts` ON `temperature_backfill`(`ts`);",
]

UPSERT_ENERGY = (
    "INSERT INTO meters_backfill (meter, ts, energy, source) VALUES (?, ?, ?, ?) "
    "ON CONFLICT(meter, ts) DO UPDATE SET energy = excluded.energy, source = excluded.source "
    "WHERE energy IS NOT excluded.energy OR source IS NOT excluded.source"
)
UPSERT_TEMPERATURE = (
    "INSERT INTO temperature_backfill (ts, temperature, source) VALUES (?, ?, ?) "
    "ON CONFLICT(ts) DO UPDATE SET temperature = excluded.temperature, source = excluded.source "
    "WHERE temperature IS NOT excluded.temperature OR source IS NOT excluded.source"
)


class CrossCheckFailed(Exception):
    def __init__(self, report: dict):
        super().__init__("cross-check failed")
        self.report = report


def read_token(path: Path, key: str) -> str:
    for line in path.read_text().splitlines():
        name, _, value = line.partition("=")
        if name.strip() == key:
            return value.strip().strip('"').strip("'")
    raise SystemExit(f"{key} not found in {path}")


def parse_not_before(value: str, floor: bool = True) -> datetime:
    """A date (local midnight Berlin) or ISO timestamp, never earlier than VALID_FROM."""
    try:
        t = datetime.fromisoformat(value)
    except ValueError:
        raise SystemExit(f"--not-before: cannot parse {value!r}")
    if t.tzinfo is None:
        t = t.replace(tzinfo=BERLIN)
    if floor and t < VALID_FROM:
        raise SystemExit(f"--not-before {value} is before {VALID_FROM.isoformat()}: the Shelly's earlier data is invalid")
    return t


def finite(v) -> bool:
    return isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v)


def accept_hours(imports: dict, returned: dict, not_before: int) -> tuple[dict[int, float], dict[str, int]]:
    """Hours (start unix seconds -> kWh) that may be backfilled, and the count of each refusal."""
    refused = {"before_bound": 0, "invalid_change": 0, "returned_energy": 0, "returned_missing": 0}
    accepted = {}
    for hour in sorted(imports):
        change = imports[hour]
        if hour < not_before:
            refused["before_bound"] += 1
        elif not finite(change) or change < 0:
            refused["invalid_change"] += 1
        elif hour not in returned or not finite(returned[hour]):
            refused["returned_missing"] += 1
        elif not 0 <= returned[hour] <= RETURNED_LIMIT:  # negative: a counter reset, unverifiable
            refused["returned_energy"] += 1
        else:
            accepted[hour] = float(change)
    return accepted, refused


def expand(hour: int, value: float) -> list[tuple[int, float]]:
    """An hour's value as its four 15-minute slots, evenly."""
    return [(hour + k * SLOT, value / (HOUR // SLOT)) for k in range(HOUR // SLOT)]


def plan_energy(accepted: dict[int, float], clean_real: set[int]) -> list[tuple[int, float]]:
    """Slots to backfill: every slot of an accepted hour without a clean heat-pump row in meters."""
    return [(ts, v) for hour in sorted(accepted) for ts, v in expand(hour, accepted[hour]) if ts not in clean_real]


def plan_temperature(hours: dict[int, float | None]) -> list[tuple[int, float]]:
    """An hourly temperature repeated for the hour's four slots; hours without a value are absent."""
    return [(hour + k * SLOT, hours[hour]) for hour in sorted(hours) if finite(hours[hour]) for k in range(HOUR // SLOT)]


def cross_check(accepted: dict[int, float], real: dict[int, float], min_days: int, max_mean: float, max_abs: float) -> dict:
    """Hourly sums of evcc's four clean slots against Home Assistant, over complete hours.

    Not enough overlap is reported, not failed: with fewer than min_days days of overlap
    nothing validates the backfill yet and the caller decides.
    """
    diffs = []
    for hour, ha in accepted.items():
        slots = [real.get(hour + k * SLOT) for k in range(HOUR // SLOT)]
        if all(s is not None for s in slots):
            diffs.append(ha - sum(slots))
    report = {"overlap_hours": len(diffs), "required_hours": min_days * 24}
    if len(diffs) < min_days * 24:
        report["verdict"] = "skipped: not enough overlap to validate"
        return report
    absolute = sorted(abs(d) for d in diffs)
    report.update(
        mean_diff=statistics.fmean(diffs),
        mean_abs_diff=statistics.fmean(absolute),
        p95_abs_diff=absolute[int(0.95 * (len(absolute) - 1))],
        max_abs_diff=absolute[-1],
    )
    if abs(report["mean_diff"]) > max_mean or report["max_abs_diff"] > max_abs:
        report["verdict"] = "REFUSED: Home Assistant and evcc do not record the same heat pump energy"
        raise CrossCheckFailed(report)
    report["verdict"] = "ok"
    return report


def resolve_entity(db: sqlite3.Connection, ref: str) -> int:
    row = db.execute('SELECT id FROM entities WHERE "group" = ? AND name = ?', ("meter", ref)).fetchone()
    if row is None:
        raise SystemExit(f"no ext meter {ref!r} in entities: add it in evcc first (and let it record once)")
    return row[0]


def real_slots(db: sqlite3.Connection, entity: int) -> dict[int, float]:
    """The heat pump's clean slots in meters (ts -> kWh): not recovered, not incomplete."""
    rows = db.execute(
        "SELECT ts, energy FROM meters WHERE meter = ? AND energy IS NOT NULL AND COALESCE(recovered,0) = 0 AND COALESCE(incomplete,0) = 0",
        (entity,),
    ).fetchall()
    return {ts: e for ts, e in rows if finite(e) and e >= 0}


def has_table(db: sqlite3.Connection, name: str) -> bool:
    return db.execute("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", (name,)).fetchone() is not None


def count_changes(db: sqlite3.Connection, table: str, entity: int, rows: list[tuple[int, float]]) -> int:
    """How many planned rows are new or differ from what the table holds."""
    if not has_table(db, table):
        return len(rows)
    if table == "meters_backfill":
        existing = dict(db.execute("SELECT ts, energy FROM meters_backfill WHERE meter = ?", (entity,)))
    else:
        existing = dict(db.execute("SELECT ts, temperature FROM temperature_backfill"))
    return sum(1 for ts, v in rows if existing.get(ts) != v)


def apply(db: sqlite3.Connection, entity: int, energy: list[tuple[int, float]], temperature: list[tuple[int, float]] | None) -> dict:
    """Write in one transaction; returns the changed row counts."""
    db.isolation_level = None
    db.execute("BEGIN")
    try:
        for statement in SCHEMA:
            db.execute(statement)
        start = db.total_changes
        db.executemany(UPSERT_ENERGY, [(entity, ts, v, SOURCE) for ts, v in energy])
        middle = db.total_changes
        if temperature is not None:
            db.executemany(UPSERT_TEMPERATURE, [(ts, v, TEMPERATURE_SOURCE) for ts, v in temperature])
        end = db.total_changes
        db.execute("COMMIT")
    except BaseException:
        db.execute("ROLLBACK")
        raise
    return {"meters_backfill": middle - start, "temperature_backfill": end - middle}


async def fetch_hours(url: str, token: str, sensors: list[str], start: datetime) -> dict[str, dict[int, float | None]]:
    import websockets

    async with websockets.connect(url, max_size=None) as ws:
        await ws.recv()
        await ws.send(json.dumps({"type": "auth", "access_token": token}))
        if json.loads(await ws.recv()).get("type") != "auth_ok":
            raise SystemExit("Home Assistant authentication failed")
        await ws.send(json.dumps({
            "id": 1, "type": "recorder/statistics_during_period", "start_time": start.astimezone(timezone.utc).isoformat(),
            "statistic_ids": sensors, "period": "hour", "types": ["change"],
        }))
        reply = json.loads(await ws.recv())
        if not reply.get("success"):
            raise SystemExit(f"statistics request failed: {reply}")
    res = {}
    for sensor in sensors:
        res[sensor] = {int(s["start"] // 1000): s.get("change") for s in reply["result"].get(sensor, []) if s["end"] - s["start"] == HOUR * 1000}
    return res


def temperature_chunks(start: date, end: date) -> list[tuple[date, date]]:
    """Inclusive date ranges of at most CHUNK_DAYS days."""
    res = []
    while start <= end:
        last = min(end, start + timedelta(days=CHUNK_DAYS - 1))
        res.append((start, last))
        start = last + timedelta(days=1)
    return res


def parse_temperature(payload: dict) -> dict[int, float | None]:
    hourly = payload["hourly"]
    return {
        int(datetime.fromisoformat(t).replace(tzinfo=timezone.utc).timestamp()): v
        for t, v in zip(hourly["time"], hourly["temperature_2m"])
    }


def fetch_temperature(url: str, lat: float, lon: float, start: date, end: date) -> dict[int, float | None]:
    res = {}
    for first, last in temperature_chunks(start, end):
        query = urllib.parse.urlencode({
            "latitude": lat, "longitude": lon, "hourly": "temperature_2m", "timezone": "UTC",
            "start_date": first.isoformat(), "end_date": last.isoformat(),
        })
        with urllib.request.urlopen(f"{url}?{query}", timeout=60) as r:
            res.update(parse_temperature(json.load(r)))
    return res


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--db", required=True, type=Path, help="evcc sqlite database (use a copy first)")
    p.add_argument("--token-file", required=True, type=Path, help="file with KEY=token lines")
    p.add_argument("--token-key", default="HA_TOKEN")
    p.add_argument("--url", default="wss://home.herold.hn/api/websocket")
    p.add_argument("--meter", required=True, help="evcc ext meter ref of the heat pump, e.g. db:19")
    p.add_argument("--import-sensor", default="sensor.shellypro3em_ac15187baa00_total_active_energy")
    p.add_argument("--returned-sensor", default="sensor.shellypro3em_ac15187baa00_total_active_returned_energy")
    p.add_argument("--not-before", default=VALID_FROM.date().isoformat(), help="first valid date (Europe/Berlin); never earlier than 2025-11-17")
    p.add_argument("--history-from", default="2025-09-01", help="fetch from here so the refused hours before --not-before are counted, never written")
    p.add_argument("--min-overlap-days", type=int, default=7)
    p.add_argument("--max-mean-diff", type=float, default=0.02, help="kWh per hour, |mean of Home Assistant - evcc|")
    p.add_argument("--max-abs-diff", type=float, default=0.15, help="kWh per hour, largest difference")
    p.add_argument("--temperature", action="store_true", help="also backfill hourly outdoor temperature (Open-Meteo)")
    p.add_argument("--lat", type=float, default=49.1579)
    p.add_argument("--lon", type=float, default=9.1737)
    p.add_argument("--temperature-url", default="https://historical-forecast-api.open-meteo.com/v1/forecast")
    p.add_argument("--dry-run", action="store_true", help="read only: report what would change")
    return p


def run(args, fetch_ha=None, fetch_temp=fetch_temperature, now: datetime | None = None) -> int:
    not_before = parse_not_before(args.not_before)
    now = now or datetime.now(timezone.utc)

    uri = f"{args.db.resolve().as_uri()}?mode={'ro' if args.dry_run else 'rw'}"
    try:
        db = sqlite3.connect(uri, uri=True)
    except sqlite3.OperationalError as e:
        raise SystemExit(f"cannot open {args.db}: {e}")
    entity = resolve_entity(db, args.meter)

    if fetch_ha is None:
        token = read_token(args.token_file, args.token_key)
        fetch_ha = lambda sensors, start: asyncio.run(fetch_hours(args.url, token, sensors, start))
    series = fetch_ha([args.import_sensor, args.returned_sensor], parse_not_before(args.history_from, floor=False))
    imports, returned = series.get(args.import_sensor, {}), series.get(args.returned_sensor, {})
    accepted, refused = accept_hours(imports, returned, int(not_before.timestamp()))

    report = {
        "meter": args.meter, "entity": entity, "dry_run": args.dry_run,
        "not_before": not_before.isoformat(), "hours_received": len(imports), "hours_accepted": len(accepted), "refused": refused,
    }
    if accepted:
        report["from"] = datetime.fromtimestamp(min(accepted), timezone.utc).isoformat()
        report["to"] = datetime.fromtimestamp(max(accepted) + HOUR, timezone.utc).isoformat()

    if not accepted:
        report["verdict"] = "REFUSED: no hour passed the checks (missing sensor, or only invalid data): nothing written"
        print(json.dumps(report, indent=2))
        print("no accepted hours: check the sensor ids and --not-before", file=sys.stderr)
        return 3
    real = real_slots(db, entity)
    try:
        report["cross_check"] = cross_check(accepted, real, args.min_overlap_days, args.max_mean_diff, args.max_abs_diff)
    except CrossCheckFailed as e:
        report["cross_check"] = e.report
        report["verdict"] = "REFUSED: nothing written"
        print(json.dumps(report, indent=2))
        return 2

    energy = plan_energy(accepted, set(real))
    report["energy_slots_planned"] = len(energy)
    report["energy_slots_skipped_real"] = len(accepted) * (HOUR // SLOT) - len(energy)

    temperature = None
    if args.temperature:
        end = (now - timedelta(days=1)).date()
        hours = fetch_temp(args.temperature_url, args.lat, args.lon, not_before.astimezone(timezone.utc).date(), end)
        temperature = plan_temperature(hours)
        report["temperature_slots_planned"] = len(temperature)

    if args.dry_run:
        report["would_change"] = {
            "meters_backfill": count_changes(db, "meters_backfill", entity, energy),
            "temperature_backfill": count_changes(db, "temperature_backfill", entity, temperature or []),
        }
        report["verdict"] = "dry run: nothing written"
    else:
        report["changed"] = apply(db, entity, energy, temperature)
        report["verdict"] = "applied"
    print(json.dumps(report, indent=2))
    return 0


def main() -> int:
    return run(build_parser().parse_args())


if __name__ == "__main__":
    sys.exit(main())
