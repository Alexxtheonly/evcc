"""Writes heating_degree_parity.json: a synthetic February-shaped house and the
Python backtest harness's T3dg (daily gate r >= 0.3 with the slot-slope > 0
rule, cap 2 kWh / 25 %) prediction for it, for TestHeatingDegreeParity.

Run from the forecast harness (evcc fork, branch research/demand-temperature):

    cd forecast && uv run python <path>/heating_degree_parity.py <path>/heating_degree_parity.json

Shape from the demand temperature report's February numbers: about 30 kWh per
day, a heat pump share of about 10.7 kWh per day at a 5.3 °C mean, day-to-day
temperature swings of several kelvin, some gaps and two incomplete days.
"""

import json
import sys

import numpy as np
import pandas as pd

from evcc_forecast import baselines, slots
from evcc_forecast.demand_temp_t3 import B0T3, Variant, daily_r

rng = np.random.default_rng(7)
tz = slots.TZ
issue = pd.Timestamp("2026-02-20 00:00", tz=tz).tz_convert("UTC")
start = pd.Timestamp("2026-01-15 00:00", tz=tz).tz_convert("UTC")
idx = pd.date_range(start, issue + pd.Timedelta(days=2), freq=slots.SLOT, inclusive="left")
loc = idx.tz_convert(tz)
day = np.asarray((loc.normalize() - loc.normalize()[0]).days)
hour = np.asarray(loc.hour + loc.minute / 60, dtype=float)

# daily mean temperature 5.3 °C +- swings, diurnal +-3 K
daily_mean = 5.3 + 4.5 * np.sin(day * 0.9) + 2.0 * np.cos(day * 2.3)
temp = daily_mean + 3.0 * np.sin((hour - 9) / 24 * 2 * np.pi) + rng.normal(0, 0.3, len(idx))
temp = np.round(temp, 1)

# base load (Wh per slot): about 19 kWh/day with a cooking peak, plus the heat pump
base = 160 + 90 * np.exp(-(((hour - 12.5) / 1.2) ** 2)) + 120 * np.exp(-(((hour - 18.5) / 1.5) ** 2))
hp = 11.5 * np.maximum(0, 15 - daily_mean) + 4.0 * np.maximum(0, 15 - temp)
y = base + hp + rng.normal(0, 40, len(idx))
y = np.maximum(0, y)

# gaps: the daily 04:15-04:30 drop, two incomplete days, scattered slots
y[(loc.hour == 4) & (loc.minute == 15)] = np.nan
for d in (5, 17):
    y[(day == d) & (loc.hour < 6)] = np.nan
y[rng.random(len(idx)) < 0.01] = np.nan
# no measured value just before the issue: no measured-slot blend in the harness
y[idx == issue - slots.SLOT] = np.nan
# the future is unknown to both
future = idx >= issue
y_hist = y.copy()
y_hist[future] = np.nan

ys = pd.Series(y_hist, index=idx)
ts = pd.Series(np.where(future, np.nan, temp), index=idx)

# a cold spell in the forecast: -8 °C on the first day (the cap binds), 8 °C on the second
targets = idx[future][1:]  # slot 0 is the issue slot
tl = targets.tz_convert(tz)
temp_fc = np.where(tl.normalize() == tl.normalize()[0], -8.0, 8.0) + 2.0 * np.sin(np.asarray(tl.hour - 9, dtype=float) / 24 * 2 * np.pi)
temp_fc = np.round(temp_fc, 1)

raw = baselines.B0(ys, ts, with_hdd=False).predict(issue, targets, temp_fc)[:, 1]
t3dg = B0T3(ys, ts, Variant(gate="daily_pos", daily_r_min=0.3, cap=(2.0, 0.25)))
corrected = t3dg.predict(issue, targets, temp_fc)[:, 1]
fit = t3dg.issue(issue)["fit"]
assert fit is not None, "gate shut"

a = max(0, t3dg._pos(pd.Timestamp("2026-01-21 00:00", tz=tz).tz_convert("UTC")))
b = t3dg._pos(issue)
r_day, n_days = daily_r(t3dg.vals[a:b], t3dg.temp[a:b], t3dg.day_id[a:b], t3dg.day_len_by_id)

window = (idx >= pd.Timestamp("2026-01-21 00:00", tz=tz).tz_convert("UTC")) & ~future
samples = [
    {"ts": int(t.timestamp()), "energy": float(e), "temp": float(c)}
    for t, e, c in zip(idx[window], y_hist[window], temp[window])
    if not np.isnan(e)
]
out = {
    "issue": issue.isoformat(),
    "samples": samples,
    "targets": [t.isoformat() for t in targets],
    "tempForecast": temp_fc.tolist(),
    "raw": raw.tolist(),
    "expected": corrected.tolist(),
    "python": {"dailyR": r_day, "days": n_days, "slope": fit.slope, "meanHDD": fit.mean_hdd},
}
with open(sys.argv[1], "w") as f:
    json.dump(out, f)
print(f"{len(samples)} samples, daily r {r_day:.3f} on {n_days} days, slope {fit.slope:.3f} Wh/K, meanHDD {fit.mean_hdd:.3f}")
print(f"raw {raw.sum() / 1e3:.2f} kWh, corrected {corrected.sum() / 1e3:.2f} kWh over {len(targets)} slots")
