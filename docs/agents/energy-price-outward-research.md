# Energy-price and savings accounting: outward research

Research date: 2026-10-03. Primary sources checked alongside ADR-011, ADR-012 and
`energy-price-research.md`. No application source or live configuration changed.

## Price numerators and denominators

EIA defines average electricity price as corresponding revenue divided by corresponding
energy sales. Its published measure includes additional bill components, so that name does
not authorize evcc to invent missing standing charges or taxes.
[Official EIA methodology](https://www.eia.gov/tools/faqs/faq.php?id=507)

Applying that arithmetic to the recorded interval ledger gives two distinct measures:

- Purchase price: `sum(import_kWh * import_price) / sum(import_kWh)`.
- Effective whole-site variable cost: `sum(import_cost - export_credit) / sum(home_kWh + EV_kWh)`.

These formulas are design deductions, not claims that EIA publishes the second measure.
Aggregate the underlying amounts, never average daily ratios. Keep matching slot masks for
numerator and denominator. Zero denominator means unavailable. Negative prices, export
credits and negative net costs remain signed. Excluded periods are not extrapolated. The
existing inward audit identifies the exact repository flow and settlement sources.

## Continuous inventory and additive time buckets

PyPSA's storage equations explicitly carry stored energy between successive timesteps,
apply directional conversion losses and distinguish an explicit initial stock from a
cyclic initial/final condition. This supports continuous state treatment, not restarting
an independent battery model for every output bucket.
[Official storage formulation](https://docs.pypsa.org/latest/user-guide/optimization/storage/)

For the selected existing W0/W1/W2/W3 chain, retain cash costs and separate inventory
adjustments. The following is an accounting identity derived for this application:

`B(t) = value_of_actual_stock(t) - value_of_counterfactual_stock(t)`

`adjusted_saving(a,b) = cash_saving(a,b) + B(b) - B(a)`

Daily values telescope into a monthly value only when adjoining buckets use exactly the
same boundary stock value and continuous underlying trajectory. Using each day's ending
price to value that day's entire energy difference does not satisfy this identity when
prices vary. Example: inventory differences 0, 1, 0 kWh across three boundaries and daily
prices EUR 0.10 and EUR 0.50 produce daily corrections +0.10 and -0.50 despite zero monthly
endpoint difference.

Minimal implementation: simulate the selected continuous range once and derive its daily
and monthly buckets from common cumulative cash and boundary inventory states. If a
standalone day and the same day inside a month must also match, both need a shared canonical
simulation anchor; a day-local restart cannot satisfy that promise. Date-based grouping
must not itself reset battery state. A data gap or changed physics starts an explicitly
marked segment, whose unexplained inventory jump must not become credited savings.

Valuation remains an estimate. A shared boundary price makes aggregation consistent but
can introduce inventory revaluation effects. Name the valuation policy and keep these
adjustments away from the actual cash headline. No external source establishes a uniquely
correct terminal price for this household.

## EV immediate-charge comparator

Caltech's first-party charging dataset separates plug-in time, unplug time, last nonzero
charging time, delivered energy and measured charging-current series. These are different
facts; charging start is not a substitute for connection opportunity.
[ACN-Data paper, Table 1](https://ev.caltech.edu/assets/pub/ACN_Data_Analysis_and_Applications.pdf)

ACN-Sim models an EV with arrival, departure, requested energy and battery charging
capability. Its uncontrolled comparator charges active vehicles as quickly as possible,
but explicitly ignores infrastructure limits. Reusing that comparator without feasibility
checks would overstate what could have happened on shared household infrastructure.
[EV model](https://acnportal.readthedocs.io/en/latest/acnsim/models.html),
[uncontrolled algorithm source](https://raw.githubusercontent.com/zach401/acnportal/master/acnportal/algorithms/uncontrolled_charging.py)

A defensible local retrospective comparator preserves measured delivered AC energy and
requires a known connection window, historical feasible charging power, session identity,
complete actual charging and price coverage, and no unmodeled charging-point collisions
or shared-limit conflicts. Tapering or a varying attainable rate needs either preserved
historical dynamics or an explicit approximation label. Current configuration is not
historical proof. Connected alone is insufficient; absent opportunity evidence is
unavailable, not zero savings. Sessions that never charged remain an uncovered population
because this repository does not persist them, as ADR-011 records.

Keep EV timing separate from the existing world-chain sum unless all worlds are rebuilt
with a consistent EV counterfactual. The current chain fixes EV timestamps, so a separate
EV estimate does not algebraically belong in W0 minus actual. A pure grid-price comparison
can be called an EV tariff-timing estimate, but cannot claim actual savings after PV and
battery interactions. Alternatively, compare immediate versus actual EV timing within
one consistently simulated PV/battery world and disclose that conditioning explicitly.

## Rejected shortcuts

- Arithmetic averages of daily unit prices: wrong weighting.
- Dividing net site cash by imported energy and calling it purchase price: mixes export
  credits into a different denominator.
- Resetting W2 at every calendar bucket: changes the counterfactual with chart granularity.
- Independent daily terminal-price corrections summed as a monthly correction: do not
  telescope under price changes.
- Replacing missing connection time with charge start, or charging immediately at today's
  nameplate power: fabricates historical availability/capability.
- Summing an independent EV savings estimate into the existing chain or summing overlapping
  decision outcomes: attribution and overlap double counting.
- A new solver or service for these metrics: existing recorded flows and deterministic
  replay are sufficient; source evidence does not justify extra infrastructure.

Useful verification cases: unequal daily imports, zero imports with PV export, signed
negative-price costs, midnight and DST buckets, a battery carrying energy across midnight,
changing boundary prices, gaps, changed physics, an EV connected across midnight, missing
Connected, missing tariff coverage, infeasible immediate delivery and overlapping sessions.
