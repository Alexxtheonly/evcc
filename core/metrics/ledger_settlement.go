package metrics

// Savings ledger settlement (ADR-011 items 1 and 3): pricing a measured or simulated
// energy series both ways, and the realised-grid-cost figure everything else in the
// ledger hangs off. See ledger_slots.go for the honesty rules this file shares.

import (
	"context"
	"time"
)

// worldFlow is one slot's grid exchange under a given world or mode: energy bought
// from and sold to the grid, in kWh.
type worldFlow struct {
	ImportKWh, ExportKWh float64
}

// Settled prices the same energy series twice, per ADR-011's settlement section.
// PerSlot is each slot at its own realised price - the headline wherever a smart
// meter settles per-slot. PeriodAverage is the whole period's import/export volumes
// priced at the mean of the period's realised prices - the conservative default, and
// what a profile-settled contract actually bills. Settlement is a user-declared
// property this package has no way to read yet (evcc's isDynamicTariff only reports
// that a price series varies, not how the site is billed) - both numbers are always
// computed and labelled, and the caller decides which is the headline.
type Settled struct {
	PerSlot       float64 `json:"perSlot"`
	PeriodAverage float64 `json:"periodAverage"`
}

func diffSettled(a, b Settled) Settled {
	return Settled{PerSlot: a.PerSlot - b.PerSlot, PeriodAverage: a.PeriodAverage - b.PeriodAverage}
}

// settleFlows prices a per-slot flow series both ways. flows must be index-aligned
// with slots (same length, same order) - every world/actual flow series in this
// package is built by walking slots in order, so this always holds by construction.
func settleFlows(slots []slotData, flows []worldFlow) Settled {
	var perSlot, sumGrid, sumFeedIn, sumImport, sumExport float64

	for i, s := range slots {
		perSlot += flows[i].ImportKWh*s.PriceGrid - flows[i].ExportKWh*s.PriceFeedIn
		sumGrid += s.PriceGrid
		sumFeedIn += s.PriceFeedIn
		sumImport += flows[i].ImportKWh
		sumExport += flows[i].ExportKWh
	}

	var periodAverage float64
	if n := len(slots); n > 0 {
		meanGrid := sumGrid / float64(n)
		meanFeedIn := sumFeedIn / float64(n)
		periodAverage = sumImport*meanGrid - sumExport*meanFeedIn
	}

	return Settled{PerSlot: perSlot, PeriodAverage: periodAverage}
}

// actualFlows is W3: the real, measured grid import/export. This is deliberately the
// same read RealisedCost prices - both cost the identical measured series, so
// "actual" in the world chain and the standalone realised-cost figure can never
// silently diverge.
func actualFlows(slots []slotData) []worldFlow {
	out := make([]worldFlow, len(slots))
	for i, s := range slots {
		out[i] = worldFlow{ImportKWh: s.GridImportKWh, ExportKWh: s.GridExportKWh}
	}
	return out
}

// RealisedCost is item 1: the one measured number everything else in the ledger
// hangs off - Σ import(s)*p_grid(s) - export(s)*p_feedin(s), joining the grid meter
// to the tariffs table. ADR-011 rule 2 (non-negotiable, see ledger_slots.go): reads
// ONLY the grid meter and the tariffs table - no PV, no battery, no control_slots,
// and never greenShare/effectivePrice/sessions.Price/sessions.PricePerKWh.
type RealisedCost struct {
	Settled  Settled  `json:"settled"`
	Coverage Coverage `json:"coverage"`
	// Note states the invoice-comparability caveat (ADR-011 rule 7) in the payload
	// itself: this is the one figure the ledger exists to compare against a real
	// invoice, and it only ever prices the grid tariff rate.
	Note string `json:"note"`
}

// ComputeRealisedCost computes item 1 for [from,to). Returns *ErrBeforeTariffStart if
// from precedes the earliest priced tariff slot. Deliberately independent of
// ComputeChain, so a caller can get the realised figure even when the battery-physics
// derivation ComputeChain needs for W2 fails, or the site has no battery at all.
//
// feedInStatic is passed straight to buildLedgerSlots - see its doc comment. It must
// be the same value ComputeChain/ComputeLedger get, or this figure and the chain would
// be computed over different slot sets.
func ComputeRealisedCost(ctx context.Context, from, to time.Time, feedInStatic *float64) (*RealisedCost, error) {
	set, err := buildLedgerSlots(ctx, from, to, false, false, feedInStatic)
	if err != nil {
		return nil, err
	}

	return &RealisedCost{
		Settled:  settleFlows(set.Slots, actualFlows(set.Slots)),
		Coverage: set.coverage(),
		Note:     noteInvoiceComparability,
	}, nil
}
