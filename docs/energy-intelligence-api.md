# Energy intelligence API

`GET /api/config/energyintelligence` returns the current settings.
`PUT /api/config/energyintelligence` replaces them after strict JSON validation.
Both use the existing configuration authentication. Changing these settings never
changes `optimizerAutomatic`, tariffs, battery limits or an activation schedule.

```json
{
  "robust": false,
  "arrivals": false,
  "useLearnedEfficiency": false,
  "batteryWear": {},
  "batteryEnergyPlane": {},
  "batteryEfficiency": {},
  "settlementMode": "simulation",
  "settlementFrom": null
}
```

`batteryWear` maps stable home battery names (for example `db:11`) to a
nonnegative cost in the configured currency per DC kWh discharged. Missing means
unconfigured; zero is an explicit no-wear assumption. `settlementMode` is
`simulation` or `interval`; `settlementFrom` is a nullable RFC3339 timestamp. This
is a reporting assumption, never a timer that enables control.

`batteryEnergyPlane` maps battery names to `ac`, `dc` or `unknown`; omitted
means unknown. AC must only be selected for an independently verified AC energy
meter. Fronius MPPT battery readings are DC and cannot identify AC conversion
efficiency from SoC. Automatic calibration is inapplicable for DC/unknown meters.
`batteryEfficiency` maps battery names to
`{"chargeEfficiency":0.95,"dischargeEfficiency":0.94}`. Both values must be in
`(0,1]`; a declared override takes precedence over learned candidates and is
identified as configured. Missing declarations retain the existing 0.9 per direction.

The state/WebSocket key `optimizerInsights` contains:

- `updated`: RFC3339 last completed attempt; `automatic`: actual control setting.
- `status`: `ready`, `degraded`, `unavailable`; `reason`: actionable failure text.
- `settings`: the settings above.
- `profile`: household quality metadata from the metrics forecast.
- `forecast`: slot array with `start`, `end`, `homeLowWh`, `homeWh`, `homeHighWh`,
  `solarLowWh`, `solarWh`, `solarHighWh`, `gridPrice` (currency/kWh).
- `devices`: stable `key`, `name`, `title`, `kind` (`battery`, `vehicle`,
  `expectedVehicle`), `capacityKWh`, `initialSoc`, optional `arrival`, `departure`,
  and `plan` slots (`start`, `chargeWh`, `dischargeWh`, `soc`). Expected vehicles
  never receive a live control suggestion.
- `economics`: one row per battery with `name`, `chargeEfficiency`,
  `dischargeEfficiency`, `source`, optional `candidateChargeEfficiency`,
  `candidateDischargeEfficiency`, `wearPerKWh`.
  `calibrationReason` and `measurementPlane` explain unsupported candidates.
- `scenarios`: optional `status`, `reason`, `selected`, `evaluations`,
  `costLow`, `costHigh`, `batterySocLow`, `batterySocHigh`. Evaluations compare
  the same first battery action across three empirical forecast scenarios.
- `snapshotId`: optional identifier of the frozen request/result record.

Missing optional values are omitted or null, never fabricated zeros. Forecast
ranges are empirical envelopes, not a confidence guarantee. Scenario model cost
includes configured wear and terminal stored energy value.
Scenario ranges do not constitute billed savings. Historical replay endpoints
and ledger extensions are documented by the metrics implementation.
