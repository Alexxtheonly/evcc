package metrics

// Savings ledger (ADR-011): the "computation layer" entry points server/http exposes.
//
// ObjectiveValue (core/metrics/optimizer_runs.go) never appears here in a currency
// context, and nothing in this file precomputes euros into a table - the energy
// inputs already persisted in meters/tariffs/control_slots are the source of truth,
// and every figure below is recomputed from them on each request (ADR-011's
// "Alternatives rejected" section - a ledger_slots table baking in today's
// assumptions would make a later correction unauditable).

import "time"

// Ledger is the full ADR-011 savings ledger response for a period: the realised
// cost, the W0..W3 chain (with the routing/timing split when a battery is
// configured), and the per-slot decision replay.
type Ledger struct {
	From      time.Time     `json:"from"`
	To        time.Time     `json:"to"`
	Chain     Chain         `json:"chain"`
	Decisions []DecisionRow `json:"decisions"`
}

// ComputeLedger runs the full savings ledger for [from,to): the world chain (item 2),
// priced both ways (item 3), plus the per-slot battery-mode decision replay (item 4).
// The realised-cost figure (item 1) is available inside Chain.Worlds (the W3/"actual"
// entry) and via the standalone ComputeRealisedCost, which computes it independently.
func ComputeLedger(from, to time.Time) (*Ledger, error) {
	chain, err := ComputeChain(from, to)
	if err != nil {
		return nil, err
	}

	// re-derive the same slot set the chain used so the decision replay lines up
	// with exactly the slots the chain priced - see DecisionDeltas' doc comment.
	set, err := buildLedgerSlots(from, to)
	if err != nil {
		return nil, err
	}

	decisions, err := DecisionDeltas(from, to, set, chain.BatteryPhysics)
	if err != nil {
		return nil, err
	}

	return &Ledger{From: from, To: to, Chain: *chain, Decisions: decisions}, nil
}
