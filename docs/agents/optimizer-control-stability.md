# Optimizer control stability

Robust automatic planning evaluates executable first actions immediately. A successful robust
result now uses economic hysteresis instead of the additional five-minute confirmation timer.
Ordinary optimization retains that timer. Enabling the robust setting or retaining old scenario
diagnostics cannot bypass confirmation: the successful result passes its own economic decision
through the existing payback, authorization, price, freshness and device-limit gates.

The preferred action is a fresh, successfully commanded consensus across controllable home
batteries, equal to the current optimizer decision and without an external override. It must
still occur among the current feasible actions. Equivalent first-step flows are represented by
the existing deterministic canonical action, normally `normal`. No minimum dwell is imposed.

For each action, regret is its worst scenario cost difference from that scenario's best feasible
action. Retain the preferred action when its excess regret over the winner is at most:

```
0.003 * sum(abs(delta first-step charge Wh) + abs(delta first-step discharge Wh))
      * max(max(abs(grid price per Wh)), max(grid price per Wh) - min(grid price per Wh))
```

The transfer difference is AC energy. EV actions are held fixed across the compared candidates,
so they contribute zero. The comparison scales with the remaining first-slot opportunity and
currency denomination. It is a switching tolerance, not a fee added to the objective or a claimed
battery wear cost. A materially better action can replace the incumbent on the next cycle.

Robust decisions expire at the first modeled slot's end or six minutes after publication,
whichever comes first. A backward clock invalidates freshness. The ordinary timer reports
pending start, five-minute eligibility, six-minute expiry and reset reasons. Neither path
overrides current-rate revalidation, solver failure release, HEMS, external control or max SoC.

`optimizerDecision` distinguishes the candidate, confirmed optimizer mode and applied device
command consensus. Applied means the last successful command, not measured power; `unknown`
(numeric 0) includes missing commands and disagreement. A battery at its charge ceiling can
therefore report applied `hold` while the site's requested mode remains `charge`.

## Numerical grid direction

Float32 balance residuals previously made a passive solar-charge action appear to import, or a
house-supply action appear to export. This could remove `normal` from the robust candidates.
Grid direction now requires more than 0.01 W, distinct from the existing 50 W battery-action
threshold. Tests retain a real 1 W import and cover a 44-second partial slot. This tolerance is
numerical; it is not an economic hysteresis margin.

## Recorded-input calibration

The private October 3 capture contains 176 archived snapshots, not every control cycle. Re-solving
their requests with the deployed `energy-v2` companion restored `normal` in 58 snapshots. Raw
selected-action transitions fell from 121 to 7 before economic hysteresis. Three other missing
`normal` candidates received an infeasible response from the companion's base scenario and stay
excluded; `scenarios.excluded` exposes these reasons.

With recorded SoC held fixed, a sequential cost-policy sweep starting in `hold` produced:

| Fraction | Sampled mode transitions | Charge transitions | Maximum extra regret, EUR |
| --- | ---: | ---: | ---: |
| 0 | 7 | 5 | 0 |
| 0.0001 | 5 | 3 | 0.000011386 |
| 0.001 | 3 | 1 | 0.000288554 |
| 0.003 | 2 | 0 | 0.000407839 |
| 0.01 | 2 | 0 | 0.000407839 |
| 0.03 | 2 | 0 | 0.000407839 |
| 0.1 | 2 | 0 | 0.000407839 |

The default is the smallest tested fraction on this stable plateau. This is a conditional ranking
comparison, not a closed-loop simulation, annual calibration or realized bill saving. It does
not fabricate observations between archived snapshots. A separate synthetic actuator test
checks that a meaningful opportunity starts immediately, maintains evolving SoC and releases
on price changes or failure.

Reproduce with private paths; the repository stores no household history:

```sh
EVCC_CONTROL_SNAPSHOTS=/private/snapshots.json \
EVCC_CONTROL_REPLAY_OUTPUT=/private/corrected.json \
EVCC_ENERGY_SOLVER_TEST_URL=http://127.0.0.1:17050 \
go test ./core -run '^TestEnergyCapturedSnapshotReplay$' -count=1 -v

EVCC_CONTROL_SNAPSHOTS=/private/snapshots.json \
EVCC_CONTROL_REPLAY_OUTPUT=/private/corrected.json \
go test ./core -run '^TestEnergyControlCostSweep$' -count=1 -v
```

The input is the existing exported snapshot rows (`id`, `ts`, JSON-string `request`, `result`,
`quality`). Use the pinned companion from `contrib/energy-optimizer`; another solver revision is
not interchangeable evidence. Device identity and efficiency provenance are retained only in the
private input. Unit tests include a minimal recorded three-action cost matrix and separately
label synthetic physics and timing fixtures.
