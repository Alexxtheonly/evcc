package prioritizer

import (
	"fmt"
	"math"
	"sync"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/util"
)

type Prioritizer struct {
	mu     sync.Mutex
	log    *util.Logger
	demand map[loadpoint.API]float64
}

func New(log *util.Logger) *Prioritizer {
	return &Prioritizer{
		log:    log,
		demand: make(map[loadpoint.API]float64),
	}
}

func (p *Prioritizer) UpdateChargePowerFlexibility(lp loadpoint.API, rates api.Rates) {
	if power := lp.GetChargePowerFlexibility(rates); power >= 0 {
		p.mu.Lock()
		p.demand[lp] = power
		p.mu.Unlock()
	}
}

// GetChargePowerFlexibility returns the power lp may reclaim from lower-priority
// peers. available is the total power currently reachable by all loadpoints
// (charge power minus grid draw). When share is set (lp runs a sub-ordering
// strategy) it selects between two regimes per peer:
//
//   - sharing: when available covers both lp's and the peer's minimum, only the
//     peer's above-minimum power is reclaimed so the peer keeps charging at its
//     minimum - both loadpoints run from surplus with no grid import.
//   - displacement: when available cannot cover both minimums, the peer's full
//     power is reclaimed so the higher-priority loadpoint takes the single
//     available slot (e.g. the emptier car under the soc strategy).
//
// With share unset the peer is always reclaimed fully, matching the original
// priority-tier behavior.
func (p *Prioritizer) GetChargePowerFlexibility(lp loadpoint.API, available float64, share bool) float64 {
	// rank every candidate on a basis resolved per priority tier so the score
	// fractions compared below share one scale (see effectiveBasis)
	candidates := p.candidates(lp)
	score := lp.EffectivePriorityScore(p.effectiveBasis(lp, candidates))

	// hysteresis deadband (soc-% -> score fraction): only outrank another loadpoint
	// when ahead by more than the band, so near-equal soc loadpoints tie and converge
	// instead of leapfrogging each other as their soc crosses. capped below 1.0 so it
	// never weakens cross-tier (integer priority) ordering.
	band := math.Min(float64(lp.GetPriorityHysteresis())/100, 0.99)

	var lpMin float64
	if share {
		lpMin = lp.EffectiveMinPower()
	}

	var (
		reduceBy float64
		msg      string
	)

	for other, power := range p.demand {
		otherScore := other.EffectivePriorityScore(p.effectiveBasis(other, candidates))
		if score-otherScore > band && power > 0 {
			reclaim := power
			// keep the peer at its minimum when both fit within the available
			// power, otherwise reclaim it fully to free the single slot.
			if share && other.GetMode() == api.ModePV && available >= lpMin+other.EffectiveMinPower() {
				reclaim = max(0, power-other.EffectiveMinPower())
			}
			reduceBy += reclaim
			msg += fmt.Sprintf("%.0fW from %s at prio %.2f, ", reclaim, other.GetTitle(), otherScore)
		}
	}

	if p.log != nil && reduceBy > 0 {
		p.log.DEBUG.Printf("lp %s at prio %.2f gets additional %stotal %.0fW\n", lp.GetTitle(), score, msg, reduceBy)
	}

	return reduceBy
}

// candidates returns the loadpoints that participate in ranking: the target plus
// every loadpoint that has registered demand.
func (p *Prioritizer) candidates(lp loadpoint.API) []loadpoint.API {
	res := []loadpoint.API{lp}
	for other := range p.demand {
		if other != lp {
			res = append(res, other)
		}
	}
	return res
}

// effectiveBasis returns the priority basis to score lp with. The energy basis
// ranks by absolute kWh while the percent basis ranks by soc-%; their fractions
// are not comparable, so a whole priority tier must use a single basis. When any
// energy-basis loadpoint in lp's tier has no known vehicle capacity (its energy
// score would silently fall back to a percentage), the entire tier is ranked by
// percent so configured and unconfigured vehicles are never mixed across scales.
func (p *Prioritizer) effectiveBasis(lp loadpoint.API, candidates []loadpoint.API) api.PriorityBasis {
	if lp.GetPriorityBasis() != api.PriorityBasisEnergy {
		return lp.GetPriorityBasis()
	}

	tier := priorityTier(lp)
	for _, other := range candidates {
		if priorityTier(other) != tier || other.GetPriorityBasis() != api.PriorityBasisEnergy {
			continue
		}
		if v := other.GetVehicle(); v == nil || v.Capacity() <= 0 {
			return api.PriorityBasisPercent
		}
	}

	return api.PriorityBasisEnergy
}

// priorityTier is the integer (cross-tier) part of a loadpoint's score. The
// strategy sub-ordering lives in the fraction and is basis-independent, so any
// basis yields the same tier.
func priorityTier(lp loadpoint.API) int {
	return int(math.Floor(lp.EffectivePriorityScore(api.PriorityBasisPercent)))
}
