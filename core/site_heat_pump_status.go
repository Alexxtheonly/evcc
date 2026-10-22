package core

import (
	"slices"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/server/db"
)

// heatPumpShadowScore compares hp-split with one other variant over one window type. Delta is
// the mean of |hp-split error| - |other error| (negative: hp-split was better) with its 95 %
// moving-block bootstrap interval.
type heatPumpShadowScore struct {
	Window      string   `json:"window"`     // day (issued 00:00) or overnight (17:00-07:00, issued from 12:00)
	Versus      string   `json:"versus"`     // profile, on or auto
	MaxMissing  int      `json:"maxMissing"` // slots a window may lack and still count
	Windows     int      `json:"windows"`    // complete windows scored
	Required    int      `json:"required"`
	Ready       bool     `json:"ready"`
	MAEHeatPump *float64 `json:"maeHeatPumpKWh,omitempty"`
	MAEOther    *float64 `json:"maeOtherKWh,omitempty"`
	Delta       *float64 `json:"deltaKWh,omitempty"`
	CILow       *float64 `json:"ciLowKWh,omitempty"`
	CIHigh      *float64 `json:"ciHighKWh,omitempty"`
}

// HeatPumpDecision is the pre-registered switch rule (ADR-015 amendment) applied to the scores.
// It is informative only: nothing in the planner reads it.
type HeatPumpDecision struct {
	Rule              string `json:"rule"`
	Required          int    `json:"required"` // complete windows per window type and comparison
	Ready             bool   `json:"ready"`    // every comparison has them
	BeatsProfile      bool   `json:"beatsProfile"`
	BeatsHeatingDegOn bool   `json:"beatsHeatingDegreeOn"`
}

const heatPumpDecisionRule = "hp-split switches on only if, over at least `required` complete windows of both types (day, overnight), " +
	"the 95 % interval of the paired error difference lies below 0 against both the uncorrected profile and heating-degree on; " +
	"beating only the profile parks the split, heating-degree on stays. The windows of every season count: " +
	"restricting the evaluation to winter heating days is not enforced here"

// HeatPumpFit is the daily regression behind the model.
type HeatPumpFit struct {
	A         float64 `json:"a"` // kWh per day at heating-degree 0
	B         float64 `json:"b"` // kWh per K and day
	R         float64 `json:"r"`
	Days      int     `json:"days"`
	HDDRange  float64 `json:"hddRange"`
	ValidDays int     `json:"validDays"`
}

// HeatPumpModelStatus is the state of the model's gates.
type HeatPumpModelStatus struct {
	Open         bool         `json:"open"`
	Reason       string       `json:"reason,omitempty"`
	Fit          *HeatPumpFit `json:"fit,omitempty"`
	ShapeDays    int          `json:"shapeDays"`
	ShapeLearned bool         `json:"shapeLearned"` // false: uniform shape
	DroppedSlots int          `json:"droppedSlots"` // negative-residual slots left out of the non-heat-pump profile
	PairedSlots  int          `json:"pairedSlots"`
	Unpaired     int          `json:"unpairedSlots"` // household slots without a clean heat-pump reading
}

// HeatPumpThresholds are the gate parameters in effect.
type HeatPumpThresholds struct {
	MinDays        int     `json:"minDays"`
	MinHDDRange    float64 `json:"minHDDRange"`
	MinR           float64 `json:"minR"`
	MinSlope       float64 `json:"minSlope"`
	MaxSlope       float64 `json:"maxSlope"`
	RecentDays     int     `json:"recentDays"`
	RecentMinDays  int     `json:"recentMinDays"`
	ShapeDays      int     `json:"shapeDays"`
	ShapeMinDays   int     `json:"shapeMinDays"`
	DayFill        float64 `json:"dayFill"`
	SigmaK         float64 `json:"sigmaK"`
	MaxMeterAgeMin float64 `json:"maxMeterAgeMinutes"`
	MaxDropped     float64 `json:"maxDroppedShare"`
	BaseTemp       float64 `json:"baseTemp"`
}

// HeatPumpStatus is the read-only state of the heat-pump split for Plan > Details.
type HeatPumpStatus struct {
	Mode        string                `json:"mode"` // off, shadow or on
	Meter       string                `json:"meter"`
	Model       HeatPumpModelStatus   `json:"model"`
	Updated     *time.Time            `json:"updated,omitempty"` // of the per-date breakdown
	Days        []heatPumpPlanDay     `json:"days"`
	Thresholds  HeatPumpThresholds    `json:"thresholds"`
	Shadow      []heatPumpShadowScore `json:"shadow"`
	Decision    HeatPumpDecision      `json:"decision"`
	Error       string                `json:"error,omitempty"`
	ShadowError string                `json:"shadowError,omitempty"`
}

// HeatPump reports the model's state. In mode off it reads nothing.
func (site *Site) HeatPump() HeatPumpStatus { return site.heatPump(time.Now()) }

func (site *Site) heatPump(now time.Time) HeatPumpStatus {
	cfg := site.GetEnergyIntelligenceSettings()
	res := HeatPumpStatus{
		Mode:  heatPumpModelMode(cfg.HeatPumpModel),
		Meter: cfg.HeatPumpMeter,
		Thresholds: HeatPumpThresholds{
			MinDays: heatPumpMinDays, MinHDDRange: heatPumpMinHDDRange, MinR: heatPumpMinR, MinSlope: heatPumpMinSlope, MaxSlope: heatPumpMaxSlope,
			RecentDays: heatPumpRecentDays, RecentMinDays: heatPumpRecentMin, ShapeDays: heatPumpShapeDays, ShapeMinDays: heatPumpShapeMinDays,
			DayFill: heatingDegreeDayFill, SigmaK: heatPumpSigmaK, MaxMeterAgeMin: heatPumpMaxMeterAge.Minutes(), MaxDropped: heatPumpMaxDropped,
			BaseTemp: heatingDegreeBaseTemp,
		},
		Days:     []heatPumpPlanDay{},
		Shadow:   []heatPumpShadowScore{},
		Decision: HeatPumpDecision{Rule: heatPumpDecisionRule, Required: heatingDegreeScoreMinWindows},
	}
	if res.Mode == heatPumpOff {
		res.Model.Reason = heatPumpOff
		return res
	}
	if res.Meter == "" {
		res.Model.Reason = heatPumpNoMeter
	} else if db.Instance != nil {
		a, err := site.heatPumpDailyAnalysis(res.Meter, now)
		if err != nil {
			res.Error = err.Error()
		}
		if a != nil {
			res.Model.Open, res.Model.Reason = a.Open, a.Reason
			res.Model.ShapeDays, res.Model.ShapeLearned = a.ShapeDays, a.ShapeLearned
			if a.Reason != heatPumpNoMeter && a.Reason != heatPumpError {
				res.Model.Fit = &HeatPumpFit{A: a.Fit.A, B: a.Fit.B, R: a.Fit.R, Days: a.Fit.Days, HDDRange: a.Fit.HDDRange, ValidDays: a.Fit.Valid}
			}
		}
	}

	l := &site.heatPumpLast
	l.mu.Lock()
	if !l.updated.IsZero() {
		updated := l.updated
		res.Updated, res.Days = &updated, slices.Clone(l.days)
		res.Model.Open, res.Model.Reason = l.open, l.reason
		res.Model.DroppedSlots, res.Model.PairedSlots, res.Model.Unpaired = l.stats.Dropped, l.stats.Pairs, l.stats.Unpaired
	}
	l.mu.Unlock()
	if res.Days == nil {
		res.Days = []heatPumpPlanDay{}
	}

	if db.Instance == nil {
		res.ShadowError = "database offline"
		return res
	}
	variants := []string{heatPumpVariant, profileVariant, heatingDegreeOn, heatingDegreeAuto}
	windows, err := metrics.HomeForecastShadowWindows(heatingDegreeSnapshotWindow, variants, now)
	if err != nil {
		res.ShadowError = err.Error()
		return res
	}
	res.Shadow, res.Decision = scoreHeatPump(shadowWindowsByType(windows))
	return res
}

// scoreHeatPump scores hp-split against the profile, heating-degree on and auto over the
// windows of each type, and applies the pre-registered rule.
func scoreHeatPump(byType map[string][]metrics.HomeForecastShadowWindow) ([]heatPumpShadowScore, HeatPumpDecision) {
	decision := HeatPumpDecision{Rule: heatPumpDecisionRule, Required: heatingDegreeScoreMinWindows, Ready: true, BeatsProfile: true, BeatsHeatingDegOn: true}
	res := []heatPumpShadowScore{}
	for _, window := range []struct {
		name       string
		maxMissing int
	}{{"day", 4}, {"overnight", 2}} {
		for _, versus := range []string{profileVariant, heatingDegreeOn, heatingDegreeAuto} {
			p := scoreShadowPair(window.maxMissing, byType[window.name], heatPumpVariant, versus)
			res = append(res, heatPumpShadowScore{
				Window: window.name, Versus: versus, MaxMissing: window.maxMissing, Windows: p.Windows, Required: heatingDegreeScoreMinWindows, Ready: p.Ready,
				MAEHeatPump: p.MAEA, MAEOther: p.MAEB, Delta: p.Delta, CILow: p.CILow, CIHigh: p.CIHi,
			})
			better := p.Ready && *p.CIHi < 0
			decision.Ready = decision.Ready && p.Ready
			switch versus {
			case profileVariant:
				decision.BeatsProfile = decision.BeatsProfile && better
			case heatingDegreeOn:
				decision.BeatsHeatingDegOn = decision.BeatsHeatingDegOn && better
			}
		}
	}
	return res, decision
}
