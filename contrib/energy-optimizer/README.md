# Local energy optimizer

This companion applies `upstream.patch` to upstream optimizer commit
`a50b33da38e5ee46c23ec744cd72fcff8b3b3c3d`. The Python dependency lock belongs to
that commit. The image build fetches that exact source, verifies the patch, and
runs both the upstream suite and the extension's physical/economic tests before
producing the runtime image. No adjacent repository or local Go replacement is
needed.

Build from this directory:

```sh
docker buildx build --platform linux/amd64 --load -t evcc-optimizer:energy-v2 .
```

Run on the evcc stack's private network, with evcc's `OPTIMIZER_URI` pointing to
`http://evcc-optimizer:7050`. This service has no hardware access, persistent data,
host port or credentials. The deployment must keep automatic control settings
unchanged. A solver failure leaves the application responsible for releasing
stale suggestions. The existing Gunicorn worker reaper terminates CBC children
when a worker exceeds its wall-clock timeout.

## API version 1

`GET /optimize/capabilities` returns:

```json
{
  "version": 1,
  "features": ["battery_availability", "battery_efficiency", "battery_wear", "fixed_first_step"]
}
```

`POST /optimize/charge-schedule` retains the upstream request shape. Optional
fields on each battery:

| Field | Units and behavior |
| --- | --- |
| `availability` | Boolean array, one value per slot. False forbids both charge and discharge. Missing means available throughout. |
| `eta_c`, `eta_d` | Finite fractions in `(0, 1]`; missing uses the request-wide efficiency. |
| `wear_cost` | Nonnegative currency per **DC Wh discharged**. `0.03 EUR/kWh` in evcc becomes `0.00003 EUR/Wh` here. Defaults to zero. |
| `first_step_charge`, `first_step_discharge` | Paired exact first-slot **AC Wh** values. Both must be supplied together. |

The response adds `wear_cost`, the total currency cost of wear. For each battery
and slot, wear is `wear_cost * discharging_power / eta_d`. It enters the primary
money objective, not the secondary strategy preferences, and is subtracted from
the reported economic objective. Negative import/export and terminal inventory
prices remain valid. Wear itself must never be negative.

`objective_value` equals export revenue minus import cost and wear, plus the value
of final minus **initial** battery energy, minus any configured demand-rate
cost. The upstream first-slot ending energy was an incorrect initial inventory;
the patch fixes it. Upstream fixture checks recompute expected economics from
their recorded grid flows and final-minus-initial inventory. Their legacy scalar
can disagree with their own energy flows, depending on the first-slot schedule.
The original tight tolerance and strict dispatch assertions remain in place.
Separate extension tests assert initial inventory directly. End-of-slot charge
goals now include slot zero.
Goals and grid limits retain the upstream soft-penalty behavior. Availability,
physical capacity, power limits and fixed first-slot actions are hard constraints.

Malformed inputs return HTTP 400. A physically infeasible fixed action returns
400 when detected before solving, or 422 when detected by the model. No failed
solve returns a usable-looking empty schedule. Inputs are bounded to 384 slots
and 16 batteries. All series must have equal lengths, all numeric data must be
finite, and durations must be positive. Optional extension fields must be omitted
rather than supplied as null.

Fixed first-step energy is normalized only for float64 arithmetic roundoff at a
computed power or storage limit: at most four ULPs, additionally capped at
`1e-9 Wh`. Accepted excess is clamped to the bound; full/empty cancellation may
move the action inward by at most four representable float64 steps. Negative
inputs, simultaneous charge/discharge and unavailable actions remain strict.
Float32 solver-output precision is not an input tolerance: controllers must
reconstruct executable full-power actions from their original request limits.

The extended OpenAPI contract is tracked in `upstream.patch` and installed at
`/app/openapi.yaml` in the image. evcc checks capabilities before transmitting
availability constraints; an incompatible hosted endpoint must not silently
discard them.

## Local verification

In a fresh checkout at the pinned revision, apply `upstream.patch`, then:

```sh
git apply /absolute/path/to/contrib/energy-optimizer/upstream.patch
uv sync --locked
uv run pytest tests /absolute/path/to/contrib/energy-optimizer/tests
uv run ruff check src tests /absolute/path/to/contrib/energy-optimizer/tests
```

On Apple Silicon, install a native `cbc` executable on PATH. The pinned PuLP
wheel includes a macOS Intel executable; the optimizer already prefers a system
CBC when available. Production pins CBC 2.10.10 using the upstream checksum.

`tests/replay_site_inputs.py` optionally accepts a sanitized site-input JSON and
home-history JSON. It reconstructs a request from live forecast, local-time mean
demand and documented power assumptions; it is not a historical optimizer
request. Missing household quarter-hour bins use the global observed mean in
this diagnostic harness only. Raw captures must not be committed.
