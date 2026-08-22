package soc

import (
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
)

const (
	ChargeEfficiency = 0.85 // assume 85% charge efficiency

	minChargePower = 1000.0  // charge power at 100% soc (just before the vehicle stops charging)
	maxChargePower = 50000.0 // charge power up to maxChargeSoc
	maxChargeSoc   = 50.0    // soc up to which maxChargePower is available

	// power reduction per soc percent above maxChargeSoc
	powerPerSoc = (maxChargePower - minChargePower) / (100 - maxChargeSoc)

	// plausible charge efficiency bounds for a learned or seeded energyPerSocStep. A real
	// session can't exceed 100% (more battery than energy delivered), and even a cold-weather
	// session with heavy preconditioning/AC losses rarely drops below 50% - a value outside
	// this band is more likely a bad reading (glitched soc, meter reset) than a real vehicle,
	// so it is rejected rather than trusted.
	minPlausibleEfficiency = 0.5
	maxPlausibleEfficiency = 1.0
)

// PlausibleEnergyPerSocStep reports whether step (Wh per soc percent) implies a charge
// efficiency within [minPlausibleEfficiency, maxPlausibleEfficiency] for the given capacity
// (Wh). Used to sanity-bound both live gradient learning and a seeded prior.
//
// step is metered (delivered) energy per soc%, while capacity/100 is the energy that actually
// ends up in the battery per soc% - a fixed physical quantity. efficiency = (capacity/100) /
// step, so step = (capacity/100) / efficiency: step is a *decreasing* function of efficiency.
// The highest plausible efficiency (100%, nothing lost) gives the *lowest* plausible step -
// delivered energy can never be less than what the battery actually stores, so step can never
// go below capacity/100. The lowest plausible efficiency gives the highest plausible step.
func PlausibleEnergyPerSocStep(step, capacity float64) bool {
	if step <= 0 || capacity <= 0 {
		return false
	}
	perStepAt100 := capacity / 100
	return step >= perStepAt100/maxPlausibleEfficiency && step <= perStepAt100/minPlausibleEfficiency
}

// BlendEnergyPerSocStep folds a newly learned gradient into a previously persisted one. A
// single session - however clean - must not fully overwrite the running estimate; the 70/30
// weighting keeps the estimate responsive to real drift (e.g. seasonal efficiency change)
// while damping the effect of any one noisy session.
func BlendEnergyPerSocStep(prior, learned float64) float64 {
	const priorWeight = 0.7
	return prior*priorWeight + learned*(1-priorWeight)
}

// Estimator provides vehicle soc and charge duration
// Vehicle Soc can be estimated to provide more granularity
type Estimator struct {
	log *util.Logger

	capacity          float64 // vehicle capacity in Wh
	energyPerSocStep  float64 // energy per soc percent in Wh
	learned           bool    // energyPerSocStep was recalculated from real charging data this session
	vehicleSoc        float64 // estimated vehicle soc in %
	initialSoc        float64 // first received valid vehicle soc in %
	initialEnergy     float64 // energy counter at first valid soc in Wh
	prevSoc           float64 // vehicle soc at last soc change in %
	prevChargedEnergy float64 // charged energy at last soc change in Wh
}

// NewEstimator creates new estimator
func NewEstimator(log *util.Logger, vehicle api.Vehicle) *Estimator {
	capacity := vehicle.Capacity() * 1e3

	return &Estimator{
		log:              log,
		capacity:         capacity,
		energyPerSocStep: capacity / ChargeEfficiency / 100, // initial gradient taking efficiency into account
	}
}

// EnergyPerSocStep returns the current (learned, seeded or default) energy per soc step in Wh.
func (s *Estimator) EnergyPerSocStep() float64 {
	return s.energyPerSocStep
}

// Learned reports whether energyPerSocStep was recalculated from real charging data during
// this vehicle attachment, as opposed to still being the constructor's default or an unused
// seed. Callers use this to decide whether there is anything new worth persisting.
func (s *Estimator) Learned() bool {
	return s.learned
}

// Seed overrides the initial energy per soc step with a previously learned value, e.g. loaded
// from vehicle settings or derived from session history. Ignored if step is not plausible for
// this vehicle's capacity, so a corrupted or stale seed can't derail a fresh estimator.
func (s *Estimator) Seed(step float64) {
	if PlausibleEnergyPerSocStep(step, s.capacity) {
		s.energyPerSocStep = step
	}
}

// virtualCapacity returns the estimated capacity in Wh, never below the vehicle's physical capacity
func (s *Estimator) virtualCapacity() float64 {
	return max(s.capacity, s.energyPerSocStep*100)
}

// RemainingChargeDuration returns the estimated remaining duration
func (s *Estimator) RemainingChargeDuration(targetSoc, chargePower float64) time.Duration {
	return remainingChargeDuration(targetSoc, chargePower, s.vehicleSoc, s.virtualCapacity())
}

func RemainingChargeDuration(targetSoc, chargePower, vehicleSoc, capacity float64) time.Duration {
	return remainingChargeDuration(targetSoc, chargePower, vehicleSoc, capacity*1e3/ChargeEfficiency)
}

func remainingChargeDuration(targetSoc, chargePower, vehicleSoc, virtualCapacity float64) time.Duration {
	// soc above which charge power starts to taper off
	taperSoc := 100 - (chargePower-minChargePower)/powerPerSoc

	var hours float64

	// below the taper point the vehicle charges at full power
	if vehicleSoc < taperSoc {
		hours += (min(targetSoc, taperSoc) - vehicleSoc) / 100 * virtualCapacity / chargePower
	}

	// above the taper point power decreases linearly towards minChargePower
	if targetSoc > taperSoc {
		hours += (targetSoc - max(vehicleSoc, taperSoc)) / 100 * virtualCapacity / ((chargePower + minChargePower) / 2)
	}

	return max(0, time.Duration(float64(time.Hour)*hours)).Round(time.Second)
}

// RemainingChargeEnergy returns the remaining charge energy in kWh
func (s *Estimator) RemainingChargeEnergy(targetSoc int) float64 {
	return remainingChargeEnergy(float64(targetSoc), s.vehicleSoc, s.virtualCapacity())
}

func RemainingChargeEnergy(targetSoc int, vehicleSoc, capacity float64) float64 {
	return remainingChargeEnergy(float64(targetSoc), vehicleSoc, capacity*1e3/ChargeEfficiency)
}

func remainingChargeEnergy(targetSoc, vehicleSoc, virtualCapacity float64) float64 {
	return max(0, targetSoc-vehicleSoc) / 100 * max(0, virtualCapacity) / 1e3
}

// Soc replaces the api.Vehicle.Soc interface to take charged energy into account
func (s *Estimator) Soc(fetchedSoc *float64, chargedEnergy float64) float64 {
	if fetchedSoc == nil {
		s.log.WARN.Println("missing vehicle soc- ignored by estimator")
		return s.vehicleSoc
	}

	chargedEnergy = max(chargedEnergy, 0)
	socDelta := *fetchedSoc - s.prevSoc
	energyDelta := chargedEnergy - s.prevChargedEnergy

	// no soc change and no energy reset: interpolate soc from charged energy
	if socDelta == 0 && energyDelta >= 0 {
		s.vehicleSoc = min(*fetchedSoc+energyDelta/s.energyPerSocStep, 100)
		s.log.DEBUG.Printf("soc estimated: %.2f%% (vehicle: %.2f%%)", s.vehicleSoc, *fetchedSoc)
		return s.vehicleSoc
	}

	s.vehicleSoc = *fetchedSoc

	if s.initialSoc == 0 {
		s.initialSoc = s.vehicleSoc
		s.initialEnergy = chargedEnergy
	}

	socDiff := s.vehicleSoc - s.initialSoc
	energyDiff := chargedEnergy - s.initialEnergy

	// recalculate gradient, wh per soc %
	if socDiff > 10 && energyDiff > 0 {
		s.energyPerSocStep = energyDiff / socDiff
		s.learned = true
		s.log.DEBUG.Printf("soc gradient updated: soc: %.1f%%, socDiff: %.1f%%, energyDiff: %.0fWh, energyPerSocStep: %.1fWh, virtualCapacity: %.0fWh", s.vehicleSoc, socDiff, energyDiff, s.energyPerSocStep, s.virtualCapacity())
	}

	// sample charged energy at soc change
	s.prevSoc = s.vehicleSoc
	s.prevChargedEnergy = chargedEnergy

	return s.vehicleSoc
}
