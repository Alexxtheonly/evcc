package metrics

// Savings ledger: the "computation layer" entry points server/http exposes.
//
// ObjectiveValue (core/metrics/optimizer_runs.go) never appears here in a currency
// context, and nothing in this file precomputes euros into a table - the energy
// inputs already persisted in meters/tariffs/control_slots are the source of truth,
// and every figure below is recomputed from them on each request. A precomputed
// ledger_slots table would bake in today's assumptions and make a later correction
// unauditable.

import (
	"context"
	"errors"
	"time"
)

// Ledger is the full savings ledger response for a period: the realised cost (always
// present when the request itself is valid), the W0..W3 chain (with the routing/timing
// split when a battery is configured), and the per-slot decision replay.
//
// Chain is nil, with ChainUnavailable explaining why, when the chain specifically
// couldn't be computed for a battery-physics reason (ErrBatteryPhysicsUnavailable,
// ErrSocGap or ErrBatteryRateCeilingUnavailable) - Realised and Decisions (mode/veto
// data, just without a SlotFlowDeltaEUR figure) are still returned in that case.
// RealisedCost is deliberately independent of the chain: the one measured number
// everything else hangs off must not disappear because a derived comparison number
// couldn't be computed.
type Ledger struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// ChainEarliest is the earliest instant the chain could have a valid slot for (see
	// EarliestChainSlot), omitted when there is none. It is NOT the same bound as the
	// ErrBeforeTariffStart refusal's `earliest`: the tariffs table can start well before
	// the battery was commissioned, in which case a request inside the tariff range
	// succeeds while the chain covers a fraction of it - and every figure the card draws
	// comes from the chain. Published so a default view can land where the diagram is
	// actually drawable instead of where the prices happen to start.
	ChainEarliest    *time.Time    `json:"chainEarliest,omitempty"`
	Realised         RealisedCost  `json:"realised"`
	Chain            *Chain        `json:"chain,omitempty"`
	ChainUnavailable string        `json:"chainUnavailable,omitempty"`
	Decisions        []DecisionRow `json:"decisions"`
}

// ComputeLedger runs the full savings ledger for [from,to): the realised-cost figure,
// the world chain priced both ways, and the per-slot battery-mode decision replay.
//
// feedInStatic is the site's currently configured feed-in price, and must be non-nil
// only when that tariff declares itself time-invariant - see buildLedgerSlots and
// feedInFallback. The same value goes to every slot-set build below, so the realised
// figure and chain retain the requested period; decision outcomes may follow later slots.
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
			// a physics refusal, not a request-level failure: refuse rather than
			// fabricate, and degrade to no chain rather than a 500 that also takes
			// the realised-cost figure down with it.
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

	decisions, err := DecisionDeltas(ctx, from, to, set, phys, feedInStatic)
	if err != nil {
		return nil, err
	}

	var chainEarliest *time.Time
	if ts, err := EarliestChainSlot(ctx); err != nil {
		return nil, err
	} else if !ts.IsZero() {
		chainEarliest = &ts
	}

	return &Ledger{
		From:             from,
		To:               to,
		ChainEarliest:    chainEarliest,
		Realised:         *realised,
		Chain:            chain,
		ChainUnavailable: chainUnavailable,
		Decisions:        decisions,
	}, nil
}
