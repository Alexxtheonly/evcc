"""Reconstruct a solver request from a captured site forecast and home history.

This is a reconstruction, not a recorded optimizer request. Household demand is
the mean observed energy for the matching local quarter hour. Solar input is W.
"""

import json
import sys
from collections import defaultdict
from datetime import datetime
from pathlib import Path
from statistics import mean
from zoneinfo import ZoneInfo

from optimizer.app import app


def replay(site_path, history_path):
    site = json.loads(Path(site_path).read_text())
    history = json.loads(Path(history_path).read_text())
    zone = ZoneInfo("Europe/Berlin")
    samples = defaultdict(list)
    for row in history:
        time = datetime.fromtimestamp(row["ts"], zone)
        samples[(time.hour, time.minute // 15)].append(row["energy"] * 1000)
    solar = dict(site["forecast"]["solar"]["timeseries"])
    grid = [row for row in site["forecast"]["grid"] if row[0] in solar]
    demand = []
    for start, end, price in grid:
        time = datetime.fromtimestamp(start, zone)
        slot_samples = samples.get((time.hour, time.minute // 15))
        demand.append(mean(slot_samples) if slot_samples else mean(row["energy"] * 1000 for row in history))
    capacity = site["battery"]["capacity"] * 1000
    data = {
        "eta_c": 0.9, "eta_d": 0.9,
        "batteries": [{
            "s_capacity": capacity, "s_initial": capacity * site["battery"]["soc"] / 100,
            "s_min": capacity * 0.05, "s_max": capacity * 0.95,
            "c_min": 0, "c_max": 6400, "d_max": 6000,
            "p_a": 0, "charge_from_grid": True, "discharge_to_grid": False,
        }],
        "time_series": {
            "dt": [end - start for start, end, price in grid],
            "gt": demand,
            "ft": [solar[start] * (end - start) / 3600 for start, end, price in grid],
            "p_N": [price / 1000 for start, end, price in grid],
            "p_E": [0] * len(grid),
        },
    }
    response = app.test_client().post("/optimize/charge-schedule", json=data)
    result = response.get_json()
    assert response.status_code == 200, result
    print(json.dumps({
        "source": "reconstructed live forecast, local-time mean demand with global mean for missing bins, assumed 6.4/6.0 kW limits",
        "slots": len(grid), "status": result["status"],
        "objective": result["objective_value"],
        "import_kwh": sum(result["grid_import"]) / 1000,
        "charge_kwh": sum(result["batteries"][0]["charging_power"]) / 1000,
    }))


if __name__ == "__main__":
    replay(*sys.argv[1:])
