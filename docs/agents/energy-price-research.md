# Daily and monthly energy-price feasibility

Source audit: delivered evcc revision `5b752acf2`, 2026-10-03. Research only. No household data,
live settings or application code changed. Metric and comparison-level choices remain with the
owner; the findings below do not assume those answers.

## Reusable facts

- `GET /api/savingsledger?from=&to=` already returns realised cost, the W0/W1/W2/W3 chain,
  inventory-adjusted comparisons, coverage and decisions. Bounds must be aligned to 15-minute
  slots, increasing, no more than 400 days, and no earlier than recorded tariff history.
  There is no daily/monthly aggregation parameter. Sources:
  [handler](../../server/http_savings_ledger_handler.go),
  [response](../../core/metrics/ledger.go),
  [range validation](../../core/metrics/ledger_slots.go).
- The public cost DTOs expose monetary totals, not grid-import kWh, total consumption kWh,
  gross import cost or export revenue. The underlying slot data already contains those energy
  flows and prices. `Settled.periodAverage` is a **total cost calculated using mean prices**,
  not a price-per-kWh value. Sources:
  [settlement](../../core/metrics/ledger_settlement.go),
  [slotData and modelledLoadKWh](../../core/metrics/ledger_slots.go).
- Whole-site load is `HomeKWh + LoadpointKWh`. Home is a derived residual that already excludes
  EV charging. W0 buys that realised load from the grid; W1 adds direct PV; W2 adds surplus-only
  battery charging and deficit discharge; W3 uses measured grid exchange. All worlds hold EV
  charging at its **actual timestamps**. Therefore W2 minus W3 estimates battery-control value
  under fixed realised EV demand, not all evcc software savings. EV charging-time savings are
  explicitly unattributed. Source: [world definitions and EV caveat](../../core/metrics/ledger_worlds.go).
- `/api/history/energy` supports `aggregate=day|month` and energy totals, but has different
  filtering and Wh rounding from the ledger. Its totals cannot safely become denominators for
  ledger costs without matching the exact included slots. Sources:
  [history handler](../../server/http_history_handler.go),
  [history query](../../core/metrics/db_history.go).

## Aggregation and accounting limits

- An import-weighted purchase price needs gross import cost divided by imported kWh over the
  same slots. Net cost after export credits divided by imports is a different metric. A
  whole-site effective variable cost uses net cost divided by realised home-plus-EV demand.
  Monthly weighted prices require summed numerators divided by summed denominators, not an
  arithmetic average of daily prices. Zero denominators mean unavailable, not zero. These are
  mathematical consequences of the slot quantities in
  [settleFlows](../../core/metrics/ledger_settlement.go).
- Coverage excludes recovered, incomplete and missing inputs without extrapolation. Realised
  and chain coverage differ because the chain additionally requires loadpoint/battery inputs.
  Despite the realised-cost comment saying grid-and-tariff only, its shared slot builder still
  requires home and configured PV readings; the full ledger can also fail on missing loadpoint
  history. This is a source-confirmed limitation for an independent purchase-price display.
  Sources: [ComputeLedger](../../core/metrics/ledger.go),
  [buildLedgerSlots](../../core/metrics/ledger_slots.go).
- Raw W2 starts from measured SoC once per requested period, carries measured changes over
  excluded gaps and reports ending inventory divergence. It is **not re-anchored every day**.
  Separately requesting each day changes that starting-state treatment; their counterfactual
  totals need not equal a separately requested month. Source:
  [computeW2](../../core/metrics/ledger_worlds.go).
- Inventory-adjusted results use frozen snapshot physics where available, label legacy
  assumptions, and segment at gaps or changed physics. They value retained DC energy using the
  final nonnegative grid price times discharge efficiency, with measured SoC endpoints preferred.
  Configured wear is included only when known throughout. These are estimated comparisons,
  not invoice amounts; daily terminal valuations need not sum to the month's independently
  computed valuation. Source: [inventory accounting](../../core/metrics/ledger_inventory.go).
- Settlement mode and start date currently affect the UI label, not backend period splitting.
  A range beginning before the declared interval-billing start is labelled simulation. Both
  per-slot and period-average cost variants are always computed. Fees and VAT are present only
  if already included in the tariff; capital costs are not modeled. Sources:
  [settlement label](../../assets/js/components/Forecast/SavingsLedgerCard.vue),
  [pricing](../../core/metrics/ledger_settlement.go),
  [invoice caveat](../../core/metrics/ledger_worlds.go).
- Individual decision outcomes overlap and must not be summed into period savings. The period
  world comparison is the aggregate measure. Source:
  [aggregate comparison](../../core/metrics/ledger_worlds.go).

## Recorded guidance versus source

Homelab ADR-011 specifies the world chain and separate hardware/software attribution, but its
daily re-anchoring instruction and settlement chronology are superseded by later implementation
and ADR-012. Current source explicitly rejects daily raw-W2 resets. Treat the currently declared
billing start as an input rather than inferring it from a dynamic tariff or an old ADR.

Sources: homelab `.docs/adr/adr-011-savings-ledger-decision-replay.md` and
`.docs/adr/adr-012-energy-intelligence.md`, read alongside the source cited above.

## Decisions still needed

1. Show purchase-weighted grid price, effective whole-site variable cost, or both?
2. Show all four hardware/control levels, or focus on battery-control benefit versus ordinary
   self-consumption? The existing chain cannot support a claim covering EV scheduling savings.
3. If EV timing attribution is requested, that is an additional counterfactual scope requiring
   connection/opportunity evidence. Older `Connected` timestamps can be absent, and sessions
   that never charged are not persisted, as ADR-011 records.

Once those choices are resolved, price displays can reuse existing ledger inputs and world
definitions. Period-consistent battery inventory accounting and EV attribution remain explicit
scope boundaries, not presentation-only details.
