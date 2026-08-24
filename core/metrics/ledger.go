package metrics

// Savings ledger (ADR-011): the "computation layer" entry points server/http exposes.
//
// ObjectiveValue (core/metrics/optimizer_runs.go) never appears here in a currency
// context, and nothing in this file precomputes euros into a table - the energy
// inputs already persisted in meters/tariffs/control_slots are the source of truth,
// and every figure below is recomputed from them on each request (ADR-011's
// "Alternatives rejected" section - a ledger_slots table baking in today's
// assumptions would make a later correction unauditable).

import (
	"context"
	"errors"
	"time"
)

// Ledger is the full ADR-011 savings ledger response for a period: the realised
// cost (item 1, always present when the request itself is valid), the W0..W3 chain
// (with the routing/timing split when a battery is configured), and the per-slot
// decision replay.
//
// Chain is nil, with ChainUnavailable explaining why, when the chain specifically
// couldn't be computed for a battery-physics reason (ErrBatteryPhysicsUnavailable,
// ErrSocGap or ErrBatteryRateCeilingUnavailable) - Realised and Decisions (mode/veto
// data, just without a SlotFlowDeltaEUR figure) are still returned in that case.
// RealisedCost's own doc comment says it's
// deliberately independent of the chain; a battery-physics refusal destroying it too
// would contradict that documented independence for no reason - the "one measured
// number everything else hangs off" shouldn't disappear because a derived comparison
// number couldn't be computed.
type Ledger struct {
	From             time.Time     `json:"from"`
	To               time.Time     `json:"to"`
	Realised         RealisedCost  `json:"realised"`
	Chain            *Chain        `json:"chain,omitempty"`
	ChainUnavailable string        `json:"chainUnavailable,omitempty"`
	Decisions        []DecisionRow `json:"decisions"`
}

// ComputeLedger runs the full savings ledger for [from,to): the realised-cost figure
// (item 1), the world chain (item 2) priced both ways (item 3), and the per-slot
// battery-mode decision replay (item 4).
//
// feedInStatic is the site's currently configured feed-in price, and must be non-nil
// only when that tariff declares itself time-invariant - see buildLedgerSlots and
// feedInFallback. The same value goes to every slot-set build below, so the realised
// figure, the chain and the decision replay always cover the identical slots.
func ComputeLedger(ctx context.Context, from, to time.Time, feedInStatic *float64) (*Ledger, error) {
	realised, err := ComputeRealisedCost(ctx, from, to, feedInStatic)
	if err != nil {
		return nil, err
	}

	// one slot-set build shared by the chain and the decision replay, so they price
	// and replay the identical slots (see DecisionDeltas' doc comment) without a
	// second full buildLedgerSlots pass.
	set, err := buildLedgerSlots(ctx, from, to, true, true, feedInStatic)
	if err != nil {
		return nil, err
	}

	chain, chainErr := computeChainFromSlots(ctx, set)
	var chainUnavailable string
	if chainErr != nil {
		if errors.Is(chainErr, ErrBatteryPhysicsUnavailable) || errors.Is(chainErr, ErrSocGap) || errors.Is(chainErr, ErrBatteryRateCeilingUnavailable) {
			// a physics refusal, not a request-level failure (ADR-011 rule 4: refuse
			// rather than fabricate) - degrade to no chain rather than a 500 that
			// also takes the realised-cost figure down with it.
			chain = nil
			chainUnavailable = chainErr.Error()
		} else {
			return nil, chainErr
		}
	}

	var phys *batteryPhysics
	if chain != nil {
		phys = chain.BatteryPhysics
	}

	decisions, err := DecisionDeltas(ctx, from, to, set, phys)
	if err != nil {
		return nil, err
	}

	return &Ledger{
		From:             from,
		To:               to,
		Realised:         *realised,
		Chain:            chain,
		ChainUnavailable: chainUnavailable,
		Decisions:        decisions,
	}, nil
}
