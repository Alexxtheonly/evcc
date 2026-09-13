"""Synthetic physical and economic contracts for the pinned solver extension."""

from copy import deepcopy

import pytest

from optimizer.app import app


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
        "features": ["battery_availability", "battery_efficiency", "battery_wear", "fixed_first_step"],
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
