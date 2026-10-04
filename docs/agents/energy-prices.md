# Calendar energy prices and counterfactual savings

`GET /api/energyprices?month=YYYY-MM` is read-only. Omit `month` for the current
month in the server's configured timezone (`TZ`, normally Europe/Berlin). A
malformed or future month returns HTTP 400. A current month without a completed
quarter returns HTTP 200 with empty coverage and absent ratios. No new tables,
background tasks or dependencies are required.

`from` and `to` are calendar-month bounds; `evaluatedTo` is the lesser of month
end and the start of the current quarter. `timezone` and `today` drive calendar
navigation. `timezone` is never Go's `Local` placeholder: without `TZ` the IANA name
is read from `/etc/localtime`/`/etc/timezone`, else the current offset is sent as
`Etc/GMT±h` or `±hh:mm`. The UI falls back to the browser zone, with a note, for
any zone it cannot resolve. `days` contain elapsed local calendar days, including missing-history
days; their `to` is clipped at `evaluatedTo`. A DST transition day therefore has
92 or 100 quarter-hour slots. `earliest` is the first recorded grid tariff, not a
promise that every meter was available then.

## Actual cash and prices

- `grid.weightedPrice` = sum(import kWh × recorded import tariff) / sum(import kWh).
- `consumption.effectivePrice` = (sum(import cost) − sum(export revenue)) /
  sum(home kWh + loadpoint kWh).

Each ratio uses its own identical numerator/denominator coverage. There is no
unweighted average of daily or slot prices. Negative and zero tariffs remain
valid. With zero energy the ratio is absent, even if measured cash is zero.
The coverage distinguishes a measured zero from missing data. Amounts are never
scaled to fill missing slots.

Grid purchase needs only a clean complete grid reading and an import tariff.
Consumption additionally needs home and every loadpoint reading, and an export
price when actual export is positive. Missing PV or battery history cannot erase
these actual metrics. The existing static feed-in fallback is allowed only with
its historical corroboration guard (at least 96 recorded witnesses, all matching
the configured static tariff); it is not a current dynamic price substituted for
history. Amounts remain variable energy charges, not an invoice including fixed
charges, taxes absent from the tariff or demand charges.

A group slot needs a clean reading from every entity that had reported by then.
Entity history has no commissioning/retirement timestamps, so an entity's first
recorded reading is its commissioning evidence: a newly added meter does not
invalidate earlier history, but a gap after its first reading does. Retirement is
not inferred from missing readings; a removed-but-still-stored meter keeps later
slots invalid. Before any loadpoint has reported, loadpoint energy counts as zero
because evcc's residual home meter contained it; slots before the first PV or
battery reading lack that evidence and drop out of the world comparison. The older
savings-ledger endpoint keeps its existing partial-group semantics. Recovered/incomplete readings are excluded throughout.

## One month, one battery trajectory

`worlds` compare W0 (grid only), W1 (PV), W2 (PV plus surplus-only battery) and
W3 (measured actual exchange) over common valid slots. EV consumption follows its
actual timestamps in all four worlds; W2→W3 is not all software savings. World
weighted purchase prices use gross import cash, while effective prices use net
cash and whole-site consumption.

W2 is simulated once for the whole month. Daily flows slice that trajectory;
changing the displayed day never restarts the battery from measured midnight
SoC. Cash, energy and coverage counts sum exactly from days to month. Comparison
coverage may be smaller than the independently reported actual metrics.

`comparison.inventoryAdjusted` exposes the existing estimated boundary valuation,
its assumptions and endpoint caveats. `inventoryBenefit` is estimated W2−W3
control benefit after inventory/wear adjustment, separate from actual cash.
Daily values are differences of cumulative month-prefix valuations, using one
historical-assumptions load and the same month-start inventory. They telescope to
the month value. A daily contribution includes a change in the valuation of carried
inventory; it is not an independently replayed daily cash saving. The pure prefix
calculation uses the existing nonnegative last-price × discharge-efficiency
boundary value and starts separate inventory-neutral segments across missing
slots or changed physics. It does not award free initial stored energy. Historical
physics and measured endpoints can still be incomplete; these remain explicit
estimated/unavailable results. Missing battery physics refuses the comparison
without suppressing the actual metrics.

## Separate EV tariff-timing estimate

`evTiming` is a same-delivered-energy, completed-session tariff comparison,
**not additive to household world savings** and not a prediction of feasible
whole-house savings. It does not model PV opportunity cost, battery routing,
shared-circuit load or demand charges. `savings` = immediate tariff cost − actual
EV tariff cost; losses remain negative. The top-level field aliases monthly
totals; each bucket contains its connection-date cohort. Sessions connected
before the month belong to the preceding cohort and are never partially rebooked.

An eligible session needs genuine recorded connection/disconnection timestamps,
its entire availability inside the completed month context, a unique historical session loadpoint title matching the meter title
(`Site` stores `lp.GetTitle()` in the session), complete clean quarter-hour meter/tariff history and agreement
between metered energy and the session register within 1 Wh. The immediate
comparison integrates exactly the same energy from connection time, bounded by
disconnection. It never uses session `price`, green share or observed peak power.

Power is the frozen request `batteries[].c_max` in **watts**. Its source is
`Site.loadpointRequest` → `Loadpoint.EffectiveMaxPower`: configured current,
phases, vehicle and circuit ceilings. A unique historical snapshot device must
match the loadpoint title, or a pre-connection expected-vehicle entry must match
the session's vehicle title. Renamed/ambiguous titles refuse attribution. A
matching snapshot at or within 15 minutes before connection is required; a
post-connection snapshot cannot establish the earlier hypothetical rate.
Snapshots must then cover the entire availability with gaps no greater than one
quarter and the same positive finite cap. Overlapping connection windows are
refused because shared capacity and sub-slot meter attribution are unknown.
These strict conditions often make older sessions unavailable, especially after
the snapshot archive's 30-day/10,000-run retention. No current configuration or
observed charging peak fills those gaps.

Status is `estimated`, `partial` (some cohort sessions excluded), or `unavailable`.
No eligible sessions means absent money/energy fields, not zero savings. Reasons:
`no_completed_sessions`, `historical_evidence_unavailable`, and
`tariff_only_connection_cohort`. Comparison reasons are `no_priced_slots`,
`battery_physics_unavailable`, and `incomplete_coverage`.

## Verification

`go test ./core/metrics ./server` covers independent actual coverage, weighted
aggregation, negative and zero prices, missing meter members, missing history,
DST boundaries, month/day flow and inventory reconciliation, and an EV same-
energy comparison that becomes a loss under negative immediate tariffs. Removing
the initial frozen power snapshot makes that estimate unavailable. The initial
accounting prototype uses four slots across midnight: actual cash 0.70 on 3 kWh
imports (0.233333/kWh), 6 kWh whole-site consumption (0.116667/kWh), and cumulative
inventory-adjusted daily control benefits 0.30 + 0.10 = 0.40.
