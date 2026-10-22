# hp-backfill

Backfills the heat pump's winter history for the heat-pump forecast (`heatPumpModel`, ADR-015
amendment): Home Assistant hourly statistics of the Shelly Pro 3EM into `meters_backfill`, and
optionally Open-Meteo hourly outdoor temperature into `temperature_backfill`. evcc's `meters` is
never written. Only the heat pump's daily fit reads the two tables; the profile, the ledger and
Results never do. evcc creates both tables itself at start, so a database without them reads as
no history.

## Usage

```sh
# 1. work on a copy
cp /path/to/evcc.db /tmp/evcc-copy.db

# 2. dry run: read only, reports counts, writes nothing (not even the tables)
uv run contrib/hp-backfill/backfill_hp.py --db /tmp/evcc-copy.db --token-file .env \
    --meter db:19 --temperature --dry-run

# 3. apply to the copy, then run it again: the second run must report 0 changed rows
uv run contrib/hp-backfill/backfill_hp.py --db /tmp/evcc-copy.db --token-file .env --meter db:19 --temperature
uv run contrib/hp-backfill/backfill_hp.py --db /tmp/evcc-copy.db --token-file .env --meter db:19 --temperature
```

`--token-file` holds a `HA_TOKEN=...` line (`--token-key` to change the name). `--meter` is the
evcc ext meter ref (`entities.name`, group `meter`) of the heat pump; the tool resolves it to the
entity id and stops with a clear error if evcc has no such meter yet. Sensor ids, URL, coordinates
(default 49.1579 / 9.1737, the same as the temperature forecast) and the cross-check limits are
arguments; see `--help`.

Exit codes: 0 ok, 2 cross-check failed (nothing written), 1 any other error.

## Safety rules (enforced in code and tested)

- No hour before 2025-11-17 00:00 Europe/Berlin is ever written. `--not-before` can move the
  bound later, never earlier. The Shelly's earlier data shows phantom returned energy and is invalid.
- An hour is refused when its returned energy changed by more than 0.001 kWh or has no returned
  value (it cannot be verified), or when its import change is negative, missing or not finite.
  Refusals are counted by reason in the report (`refused`).
- Each accepted hour becomes four equal slots. Slots where `meters` already holds a clean heat-pump
  row (not recovered, not incomplete) are skipped: real data wins.
- Cross-check: once at least `--min-overlap-days` (7) days of complete hours exist in both Home
  Assistant and evcc, the hourly sums must agree within `--max-mean-diff` (0.02 kWh) and
  `--max-abs-diff` (0.15 kWh), else exit 2 with nothing written. With less overlap the report says
  `skipped`. Run the tool again after the ext meter has recorded for a week to get the check.
- Reruns are no-ops: a row is only updated when its value differs. All writes are one transaction.
- Temperature comes from the Open-Meteo *historical forecast* API in chunks of at most 90 days,
  UTC, up to yesterday. The Home Assistant heat-pump temperature sensor is not used (stuck at
  1.1 C from 2025-11-19 to 11-30).

## Verifying on the copy

```sh
sqlite3 /tmp/evcc-copy.db "select count(*), min(ts), max(ts) from meters_backfill"
sqlite3 /tmp/evcc-copy.db "select count(*) from meters_backfill b join meters m on m.meter=b.meter and m.ts=b.ts \
    where coalesce(m.recovered,0)=0 and coalesce(m.incomplete,0)=0"   # expect 0: real slots are not backfilled
sqlite3 /tmp/evcc-copy.db "select min(ts) >= strftime('%s','2025-11-16 23:00:00') from meters_backfill"  # expect 1
# the meters table is byte-for-byte the same rows as before
```

Then start evcc on the copy (or run `go test ./core/metrics/ -run HeatPump`) and open
`/api/energyplan/heatpump`: `model.fit.validDays` shows the days the fit found.

## Tests

```sh
python3 -m unittest discover -s contrib/hp-backfill
```

Offline: fixtures and a temporary sqlite only, no Home Assistant, no network.
