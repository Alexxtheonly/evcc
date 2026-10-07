"""Synthetic physical and economic contracts for the pinned solver extension."""

import math
from copy import deepcopy

import pytest

from optimizer.app import app
from optimizer.energy_extension import validate_input


@pytest.fixture
def client():
    return app.test_client()


@pytest.fixture
def request_data():
    return {
        "strategy": {"charging_strategy": "none"},
        "eta_c": 1.0,
        "eta_d": 1.0,
        "batteries": [{
            "s_capacity": 10000, "s_min": 0, "s_max": 10000,
            "s_initial": 0, "c_min": 0, "c_max": 5000, "d_max": 5000,
            "p_a": 0, "charge_from_grid": True, "discharge_to_grid": False,
        }],
        "time_series": {
            "dt": [3600, 3600, 3600], "gt": [0, 0, 1000],
            "ft": [0, 0, 0], "p_N": [0.00020, 0.00025, 0.00030],
            "p_E": [0, 0, 0],
        },
    }


def solve(client, data):
    response = client.post("/optimize/charge-schedule", json=data)
    assert response.status_code == 200, response.get_json()
    result = response.get_json()
    assert result["status"] in ("Optimal", "Feasible"), result
    return result


def test_capability_version(client):
    response = client.get("/optimize/capabilities")
    assert response.status_code == 200
    assert response.get_json() == {
        "version": 1,
        "features": ["battery_availability", "battery_efficiency", "battery_wear", "fixed_first_step",
                     "battery_band", "minimum_run"],
    }


def test_arrival_prevents_cheap_phantom_charging(client, request_data):
    battery = request_data["batteries"][0]
    battery.update(availability=[False, True, False],
                   d_max=0, s_goal=[0, 1000, 0])
    result = solve(client, request_data)
    assert result["batteries"][0]["charging_power"] == pytest.approx(
        [0, 1000, 0], abs=0.1)


def test_device_efficiency_conserves_energy(client, request_data):
    request_data["batteries"][0].update(eta_c=0.8, eta_d=0.5)
    result = solve(client, request_data)
    battery = result["batteries"][0]
    assert sum(battery["charging_power"]) == pytest.approx(0, abs=0.1)
    request_data["time_series"]["p_N"][2] = 0.001
    result = solve(client, request_data)
    battery = result["batteries"][0]
    assert sum(battery["charging_power"]) == pytest.approx(2500, abs=0.1)
    assert sum(battery["discharging_power"]) == pytest.approx(1000, abs=0.1)
    assert battery["state_of_charge"][-1] == pytest.approx(0, abs=0.1)


def test_device_efficiencies_are_independent(client, request_data):
    first = request_data["batteries"][0]
    first.update(eta_c=0.8, d_max=0, s_goal=[1000, 0, 0])
    second = deepcopy(first)
    second["eta_c"] = 1
    request_data["batteries"].append(second)
    result = solve(client, request_data)
    assert result["batteries"][0]["charging_power"][0] == pytest.approx(
        1250, abs=0.1)
    assert result["batteries"][1]["charging_power"][0] == pytest.approx(
        1000, abs=0.1)


def test_disconnected_stored_energy_is_unavailable(client, request_data):
    request_data["batteries"][0].update(
        s_initial=1000, availability=[False] * 3)
    result = solve(client, request_data)
    assert sum(result["batteries"][0]["discharging_power"]
               ) == pytest.approx(0, abs=0.1)
    assert result["grid_import"][-1] == pytest.approx(1000, abs=0.1)


def test_quarter_hour_fixed_action_is_energy(client, request_data):
    request_data["time_series"]["dt"] = [900, 900, 900]
    request_data["batteries"][0].update(
        first_step_charge=1250, first_step_discharge=0)
    result = solve(client, request_data)
    assert result["batteries"][0]["charging_power"][0] == pytest.approx(
        1250, abs=0.1)
    request_data["batteries"][0]["first_step_charge"] = 1251
    assert client.post("/optimize/charge-schedule",
                       json=request_data).status_code == 400


@pytest.mark.parametrize("direction", ["charge", "discharge"])
def test_fixed_power_boundary_normalizes_arithmetic_roundoff(client, request_data, direction):
    request_data["time_series"]["dt"][0] = 610
    request_data["time_series"]["gt"][0] = 5000
    battery = request_data["batteries"][0]
    battery.update(s_initial=5000, first_step_charge=0, first_step_discharge=0)
    expected = 5000 * 610 / 3600
    field = "first_step_" + direction
    battery[field] = 5000 * (610 / 3600)  # Go's operation ordering
    assert battery[field] > expected
    solve(client, request_data)
    validate_input(request_data)
    assert battery[field] == expected


@pytest.mark.parametrize("direction", ["charge", "discharge"])
@pytest.mark.parametrize("excess", [1e-8, 1e-4])
def test_fixed_power_boundary_rejects_real_excess(client, request_data, direction, excess):
    request_data["time_series"]["dt"][0] = 610
    battery = request_data["batteries"][0]
    battery.update(s_initial=5000, first_step_charge=0, first_step_discharge=0)
    battery["first_step_" + direction] = 5000 * 610 / 3600 + excess
    assert client.post("/optimize/charge-schedule", json=request_data).status_code == 400


@pytest.mark.parametrize("direction,initial,eta", [
    ("charge", 5386.063499054472, 0.8424100402482764),
    ("discharge", 1882.575109766443, 0.8183761115983582),
])
def test_fixed_storage_boundary_normalizes_arithmetic_roundoff(client, request_data, direction, initial, eta):
    request_data["time_series"]["gt"][0] = 5000
    battery = request_data["batteries"][0]
    battery.update(s_capacity=19320, s_max=19320, s_initial=initial,
                   c_max=30000, d_max=30000, eta_c=eta, eta_d=eta,
                   first_step_charge=0, first_step_discharge=0)
    field = "first_step_" + direction
    battery[field] = (19320 - initial) / eta if direction == "charge" else initial * eta
    original = battery[field]
    final = initial + eta * battery["first_step_charge"] - battery["first_step_discharge"] / eta
    assert not 0 <= final <= 19320  # Reproduce floating cancellation at full/empty.
    solve(client, request_data)
    validate_input(request_data)
    assert 0 <= initial + eta * battery["first_step_charge"] - battery["first_step_discharge"] / eta <= 19320
    assert 0 <= original - battery[field] <= 4 * math.ulp(original)
    battery[field] = original + 16 * math.ulp(original)
    assert client.post("/optimize/charge-schedule", json=request_data).status_code == 400


def test_wear_reverses_marginal_arbitrage(client, request_data):
    without_wear = solve(client, request_data)
    assert sum(without_wear["batteries"][0]
               ["discharging_power"]) == pytest.approx(1000, abs=0.1)
    request_data["batteries"][0]["wear_cost"] = 0.00011
    with_wear = solve(client, request_data)
    assert sum(with_wear["batteries"][0]
               ["discharging_power"]) == pytest.approx(0, abs=0.1)
    assert with_wear["objective_value"] == pytest.approx(-0.3, abs=0.0001)


def test_wear_prices_dc_discharge_and_negative_import(client, request_data):
    request_data["batteries"][0].update(eta_d=0.8, wear_cost=0.00010)
    request_data["time_series"]["p_N"][0] = -0.00001
    result = solve(client, request_data)
    assert sum(result["batteries"][0]["discharging_power"]
               ) == pytest.approx(1000, abs=0.1)
    assert result["wear_cost"] == pytest.approx(0.125, abs=0.0001)
    expected = - \
        sum(a * b for a, b in zip(result["grid_import"],
            request_data["time_series"]["p_N"])) - 0.125
    assert result["objective_value"] == pytest.approx(expected, abs=0.0001)


def test_terminal_value_includes_first_slot_charge(client, request_data):
    request_data["batteries"][0].update(p_a=0.00025, d_max=0)
    request_data["time_series"]["gt"] = [0, 0, 0]
    result = solve(client, request_data)
    stored = result["batteries"][0]["state_of_charge"][-1]
    expected = stored * 0.00025 - \
        sum(a * b for a,
            b in zip(result["grid_import"], request_data["time_series"]["p_N"]))
    assert result["objective_value"] == pytest.approx(expected, abs=0.0001)
    assert result["objective_value"] > 0.2


def test_fixed_first_step_is_exact(client, request_data):
    request_data["batteries"][0].update(
        first_step_charge=300, first_step_discharge=0)
    result = solve(client, request_data)
    assert result["batteries"][0]["charging_power"][0] == pytest.approx(
        300, abs=0.1)


def test_first_slot_departure_goal_is_enforced(client, request_data):
    request_data["batteries"][0].update(s_goal=[1000, 0, 0], d_max=0)
    result = solve(client, request_data)
    assert result["batteries"][0]["state_of_charge"][0] >= 999.9


def test_fixed_action_with_insufficient_stored_energy_is_rejected(client, request_data):
    request_data["batteries"][0].update(
        first_step_charge=0, first_step_discharge=300)
    assert client.post("/optimize/charge-schedule",
                       json=request_data).status_code == 400


def test_fixed_action_below_minimum_charging_is_infeasible(client, request_data):
    request_data["batteries"][0].update(
        c_min=1380, first_step_charge=300, first_step_discharge=0)
    response = client.post("/optimize/charge-schedule", json=request_data)
    assert response.status_code == 422


def test_unavailable_fixed_action_is_rejected(client, request_data):
    request_data["batteries"][0].update(
        availability=[False, True, True], first_step_charge=300, first_step_discharge=0)
    response = client.post("/optimize/charge-schedule", json=request_data)
    assert response.status_code == 400


@pytest.mark.parametrize("extension", [
    {"availability": [True]}, {"availability": [1, 1, 1]},
    {"eta_c": 0}, {"eta_d": 1.01}, {"eta_c": float("nan")},
    {"wear_cost": -0.1}, {"wear_cost": float("inf")},
    {"first_step_charge": 0}, {"first_step_charge": -1, "first_step_discharge": 0},
    {"first_step_charge": 1, "first_step_discharge": 1},
    {"first_step_charge": 5001, "first_step_discharge": 0},
    {"first_step_charge": -1e-13, "first_step_discharge": 0},
    {"first_step_charge": 1e-13, "first_step_discharge": 1e-13},
    {"availability": [False, True, True], "first_step_charge": 1e-13, "first_step_discharge": 0},
    {"s_band": 1000}, {"p_band": 0.0001}, {"s_band": 1000, "p_band": -0.0001},
    {"s_band": 10001, "p_band": 0.0001}, {"s_band": float("nan"), "p_band": 0.0001},
    {"s_band": 1000, "p_band": float("inf")}, {"c_min_run": 1800},
    {"c_min": 1000, "c_min_run": -1}, {"c_min": 1000, "c_min_run": 86401},
    {"c_min": 1000, "c_run_elapsed": -1}, {"c_min": 1000, "c_run_elapsed": True},
    {"band_storage_feed": False}, {"c_min": 1000, "s_band": 1000, "p_band": 0.0001, "band_storage_feed": 0},
    {"s_band": 1000, "p_band": 0.0001, "band_storage_feed": False},
])
def test_invalid_extension_is_rejected(client, request_data, extension):
    request_data["batteries"][0].update(extension)
    assert client.post("/optimize/charge-schedule",
                       json=request_data).status_code == 400


def test_omitted_fields_preserve_dispatch(client, request_data):
    legacy = solve(client, request_data)
    explicit = deepcopy(request_data)
    explicit["batteries"][0].update(
        availability=[True] * 3, eta_c=1, eta_d=1, wear_cost=0)
    extended = solve(client, explicit)
    assert extended["grid_import"] == pytest.approx(
        legacy["grid_import"], abs=0.1)
    assert extended["grid_export"] == pytest.approx(
        legacy["grid_export"], abs=0.1)


def band_request(prices, s_band, threshold, eta_c=0.9, p_a=0.0, **battery):
    """Vehicle-like battery valuing stored energy up to s_band so that grid energy pays off
    below threshold (currency/Wh, AC side): eta_c * (p_a + p_band) = threshold."""
    count = len(prices)
    vehicle = {
        "s_capacity": 55000, "s_min": 0, "s_max": 55000, "s_initial": 0,
        "c_min": 1380, "c_max": 11040, "d_max": 0, "p_a": p_a,
        "charge_from_grid": True, "discharge_to_grid": False,
        "s_band": s_band, "p_band": threshold / eta_c - p_a,
    }
    vehicle.update(battery)
    return {
        "strategy": {"charging_strategy": "none"},
        "eta_c": eta_c, "eta_d": eta_c,
        "batteries": [vehicle],
        "time_series": {
            "dt": [900] * count, "gt": [0] * count, "ft": [0] * count,
            "p_N": prices, "p_E": [0] * count,
        },
    }


def charge_runs(charging):
    runs, start = [], None
    for t, energy in enumerate(charging + [0]):
        if energy > 0.1 and start is None:
            start = t
        elif energy <= 0.1 and start is not None:
            runs.append((start, t - 1))
            start = None
    return runs


def test_band_buys_grid_only_below_reference(client):
    # 0.30 EUR/kWh reference, realistic magnitudes; the band needs more than the cheap slots hold
    prices = [0.00040, 0.00025, 0.00029, 0.00031, 0.00045, 0.00020]
    result = solve(client, band_request(prices, 30000, 0.00030))
    charging = result["batteries"][0]["charging_power"]
    assert [t for t, energy in enumerate(charging) if energy > 0.1] == [1, 2, 5]
    assert charging[1] == pytest.approx(2760, abs=0.1)  # full power, 11.04 kW for 15 min


def test_band_stops_at_its_top(client):
    data = band_request([0.00010] * 6, 5000, 0.00030)
    result = solve(client, data)
    assert result["batteries"][0]["state_of_charge"][-1] == pytest.approx(5000, abs=0.1)
    data["batteries"][0]["s_initial"] = 6000
    result = solve(client, data)
    assert sum(result["batteries"][0]["charging_power"]) == pytest.approx(0, abs=0.1)


def test_band_reference_converts_stored_energy_with_charge_efficiency(client):
    # threshold 0.30 at eta_c 0.9 means p_band 0.000333 per stored Wh; a 0.32 slot must stay
    # unused and a 0.29 slot be used, whatever p_a already contributes
    data = band_request([0.00032, 0.00029, 0.00032], 2000, 0.00030, p_a=0.00020)
    assert data["batteries"][0]["p_band"] == pytest.approx(0.0001333, abs=1e-7)
    charging = solve(client, data)["batteries"][0]["charging_power"]
    assert charging[0] == pytest.approx(0, abs=0.1)
    assert charging[2] == pytest.approx(0, abs=0.1)
    assert charging[1] == pytest.approx(2000 / 0.9, abs=0.1)


def test_band_prefers_solar_to_cheap_grid(client):
    data = band_request([0.00020, 0.00040, 0.00040], 2000, 0.00030)
    data["time_series"]["ft"] = [0, 0, 3000]
    data["time_series"]["p_E"] = [0.00008] * 3
    charging = solve(client, data)["batteries"][0]["charging_power"]
    assert charging[0] == pytest.approx(0, abs=0.1)
    assert charging[2] == pytest.approx(2000 / 0.9, abs=0.1)


def test_nothing_cheap_means_no_band_charging(client):
    result = solve(client, band_request([0.00035, 0.00040, 0.00031], 30000, 0.00030))
    assert sum(result["batteries"][0]["charging_power"]) == pytest.approx(0, abs=0.1)


def test_band_does_not_relax_the_floor(client):
    # below s_min the soft floor still charges immediately, price-blind, then waits for cheap energy
    data = band_request([0.00050, 0.00045, 0.00020, 0.00045], 3800, 0.00030, s_initial=500, s_min=1500)
    charging = solve(client, data)["batteries"][0]["charging_power"]
    assert charging[0] * 0.9 + 500 >= 1500 - 0.1
    assert charging[1] == pytest.approx(0, abs=0.1)
    assert 500 + 0.9 * sum(charging) == pytest.approx(3800, abs=0.1)


def test_band_value_enters_the_reported_objective(client):
    data = band_request([0.00020, 0.00040], 2000, 0.00030, s_initial=500)
    result = solve(client, data)
    imported = sum(a * b for a, b in zip(result["grid_import"], data["time_series"]["p_N"]))
    expected = (2000 - 500) * data["batteries"][0]["p_band"] - imported
    assert result["objective_value"] == pytest.approx(expected, abs=1e-6)


def test_minimum_run_skips_an_isolated_cheap_slot(client):
    # 1000 Wh needed: the single cheapest slot would be a 15-minute run
    prices = [0.00010, 0.00050, 0.00050, 0.00012, 0.00012, 0.00050]
    data = band_request(prices, 900, 0.00030, eta_c=0.9)
    assert charge_runs(solve(client, data)["batteries"][0]["charging_power"]) == [(0, 0)]
    data["batteries"][0]["c_min_run"] = 1800
    runs = charge_runs(solve(client, data)["batteries"][0]["charging_power"])
    assert runs == [(3, 4)]


def test_run_in_progress_continues_until_its_minimum(client):
    # 15 minutes into a 30-minute minimum: slot 0 is above the reference, but solar covers
    # the minimum power there, so the run continues instead of stopping
    prices = [0.00050, 0.00010, 0.00010, 0.00050]
    data = band_request(prices, 2000, 0.00030, c_min_run=1800, c_run_elapsed=900)
    data["time_series"]["ft"] = [400, 0, 0, 0]
    data["time_series"]["p_E"] = [0.00008] * 4
    result = solve(client, data)
    assert result["batteries"][0]["charging_power"][0] >= 345 - 0.1
    assert result["grid_import"][0] == pytest.approx(0, abs=0.1)


def test_held_run_yields_rather_than_import_above_the_reference(client):
    prices = [0.00050, 0.00010, 0.00010, 0.00050]
    data = band_request(prices, 2000, 0.00030, c_min_run=1800, c_run_elapsed=900)
    result = solve(client, data)
    assert result["batteries"][0]["charging_power"][0] == pytest.approx(0, abs=0.1)
    # the restart is a new run held to the full minimum
    assert charge_runs(result["batteries"][0]["charging_power"]) == [(1, 2)]


def test_run_past_its_minimum_may_stop(client):
    prices = [0.00050, 0.00010, 0.00010, 0.00050]
    data = band_request(prices, 2000, 0.00030, c_min_run=1800, c_run_elapsed=1800)
    assert solve(client, data)["batteries"][0]["charging_power"][0] == pytest.approx(0, abs=0.1)
    # a run that ended may restart at once, but then lasts the minimum again
    data["batteries"][0]["c_run_elapsed"] = 0
    assert charge_runs(solve(client, data)["batteries"][0]["charging_power"]) == [(1, 2)]


def test_minimum_run_is_cut_by_the_horizon_end(client):
    prices = [0.00050, 0.00050, 0.00050, 0.00050, 0.00050, 0.00010]
    data = band_request(prices, 900, 0.00030, c_min_run=3600)
    assert charge_runs(solve(client, data)["batteries"][0]["charging_power"]) == [(5, 5)]


def test_omitted_band_preserves_dispatch(client):
    data = band_request([0.00020, 0.00040, 0.00010], 2000, 0.00030, s_initial=500, s_min=1500)
    for key in ("s_band", "p_band"):
        del data["batteries"][0][key]
    legacy = solve(client, data)
    explicit = deepcopy(data)
    explicit["batteries"][0].update(c_run_elapsed=0)
    assert solve(client, explicit) == legacy


def storage_request(**vehicle):
    """A full home battery and a band vehicle, grid above the band reference everywhere."""
    data = band_request([0.00040] * 4, 4000, 0.00030, **vehicle)
    data["time_series"]["gt"] = [300] * 4
    data["batteries"].append({
        "s_capacity": 19320, "s_min": 966, "s_max": 19320, "s_initial": 19320,
        "c_min": 0, "c_max": 6000, "d_max": 6000, "p_a": 0,
        "charge_from_grid": True, "discharge_to_grid": False,
    })
    return data


def test_storage_feeds_the_band_unless_refused(client):
    # worthless stored energy (p_a 0) is worth charging into the car's band
    fed = solve(client, storage_request())
    assert sum(fed["batteries"][0]["charging_power"]) > 1000
    data = storage_request(band_storage_feed=False)
    result = solve(client, data)
    car = result["batteries"][0]["charging_power"]
    home = result["batteries"][1]["discharging_power"]
    assert sum(car) == pytest.approx(0, abs=0.1)
    assert all(d == pytest.approx(300, abs=0.1) for d in home)  # the household only


def test_refused_storage_feed_holds_the_battery_while_the_band_charges(client):
    data = storage_request(band_storage_feed=False)
    data["time_series"]["p_N"] = [0.00040, 0.00020, 0.00040, 0.00040]  # cheap below the reference
    result = solve(client, data)
    car = result["batteries"][0]["charging_power"]
    home = result["batteries"][1]["discharging_power"]
    assert car[1] > 1000
    assert home[1] == pytest.approx(0, abs=0.1)  # hold: the inverter would feed the car too
    assert result["grid_import"][1] == pytest.approx(car[1] + 300, abs=0.1)


def test_storage_feed_flag_true_is_the_default(client):
    data = storage_request()
    explicit = deepcopy(data)
    explicit["batteries"][0]["band_storage_feed"] = True
    assert solve(client, explicit) == solve(client, data)


def surplus_request(price):
    """900 W of surplus (225 Wh per quarter hour over a 300 Wh household) and a car in its band
    with a 0.36 reference; the car's minimum power is 1380 W."""
    data = band_request([price] * 4, 27500, 0.00036063, s_initial=19800, s_min=16500, p_a=0.000279)
    data["time_series"]["gt"] = [300] * 4
    data["time_series"]["ft"] = [525] * 4
    data["time_series"]["p_E"] = [0.0000786] * 4
    return data


def test_band_waits_rather_than_top_up_surplus_with_expensive_grid(client):
    result = solve(client, surplus_request(0.00060))
    assert sum(result["batteries"][0]["charging_power"]) == pytest.approx(0, abs=0.1)
    assert sum(result["grid_import"]) == pytest.approx(0, abs=0.1)


def test_band_tops_up_surplus_with_grid_below_the_reference(client):
    result = solve(client, surplus_request(0.00030))
    assert all(c >= 345 - 0.1 for c in result["batteries"][0]["charging_power"])


def test_band_grid_rule_keeps_the_floor(client):
    data = surplus_request(0.00060)
    data["batteries"][0].update(s_initial=15000)
    data["time_series"]["ft"] = [0] * 4
    result = solve(client, data)
    stored = result["batteries"][0]["state_of_charge"][-1]
    assert stored == pytest.approx(16500, abs=0.5)  # the floor and not a Wh more


def test_band_grid_rule_does_not_bind_plans(client):
    data = surplus_request(0.00060)
    data["time_series"]["ft"] = [0] * 4
    data["batteries"][0]["s_goal"] = [0, 0, 0, 21000]
    result = solve(client, data)
    assert result["batteries"][0]["state_of_charge"][-1] >= 21000 - 0.5
