package metrics

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"gorm.io/gorm/clause"
)

// Shadow forecasts (ADR-015): household forecasts that were computed but not used
// for planning, kept beside home_forecast_samples so a variant can be scored live
// against the one in use. Nothing here feeds back into planning or into the range
// calibration, which reads home_forecast_samples only.

// homeForecastShadowSample is one archived shadow prediction (kWh).
type homeForecastShadowSample struct {
	Slot        int64  `gorm:"uniqueIndex:home_forecast_shadow_slot_lead_variant"`
	LeadMinutes int    `gorm:"uniqueIndex:home_forecast_shadow_slot_lead_variant"`
	Variant     string `gorm:"uniqueIndex:home_forecast_shadow_slot_lead_variant"`
	Issued      int64
	Base        float64
	Low         float64
	High        float64
}

// homeForecastShadowRetention matches the retention of home_forecast_samples.
const homeForecastShadowRetention = 90

// ArchiveHomeForecastShadow archives a variant's forecast issued at issued: the
// slots at ForecastLeadTimes like ArchiveHomeForecast and, with horizon, every
// future slot at its actual lead (a snapshot for scoring whole windows).
func ArchiveHomeForecastShadow(issued time.Time, variant string, slots []HomeForecastSlot, horizon bool) error {
	if variant == "" {
		return errors.New("missing shadow forecast variant")
	}
	sample := func(s HomeForecastSlot, lead int) (homeForecastShadowSample, error) {
		if !finite(s.Base) || !finite(s.Low) || !finite(s.High) || s.Low < 0 || s.Low > s.Base || s.Base > s.High {
			return homeForecastShadowSample{}, fmt.Errorf("invalid %s shadow forecast sample", variant)
		}
		return homeForecastShadowSample{Slot: s.Start.Unix(), LeadMinutes: lead, Variant: variant, Issued: issued.Unix(), Base: s.Base / 1e3, Low: s.Low / 1e3, High: s.High / 1e3}, nil
	}

	var samples []homeForecastShadowSample
	for _, lead := range ForecastLeadTimes {
		target := issued.Add(lead).Truncate(tariff.SlotDuration)
		for _, s := range slots {
			if s.Start.Equal(target) && s.Start.After(issued) {
				v, err := sample(s, int(lead.Minutes()))
				if err != nil {
					return err
				}
				samples = append(samples, v)
				break
			}
		}
	}
	if horizon {
		for _, s := range slots {
			if !s.Start.After(issued) {
				continue
			}
			v, err := sample(s, int(s.Start.Sub(issued).Minutes()))
			if err != nil {
				return err
			}
			samples = append(samples, v)
		}
	}

	if len(samples) > 0 {
		if err := db.Instance.Clauses(clause.OnConflict{DoNothing: true}).Create(&samples).Error; err != nil {
			return err
		}
	}
	return db.Instance.Where("slot < ?", issued.AddDate(0, 0, -homeForecastShadowRetention).Unix()).Delete(new(homeForecastShadowSample)).Error
}

// horizonSnapshots selects the (issued, variant) pairs written with horizon: a
// lead-only archive writes at most len(ForecastLeadTimes) rows per issue.
func horizonSnapshots() string {
	return fmt.Sprintf(`SELECT issued, variant FROM home_forecast_shadow_samples GROUP BY issued, variant HAVING COUNT(*) > %d`, len(ForecastLeadTimes))
}

// HomeForecastShadowSnapshotExists reports whether a horizon snapshot was issued
// in [from, to).
func HomeForecastShadowSnapshotExists(from, to time.Time) (bool, error) {
	var n int64
	err := db.Instance.Raw(`SELECT COUNT(*) FROM (`+horizonSnapshots()+`) WHERE issued >= ? AND issued < ?`, from.Unix(), to.Unix()).Scan(&n).Error
	return n > 0, err
}

// HomeForecastShadowWindow compares two variants' energy totals over one window
// against the measured household energy (kWh). Sums run over the slots where the
// measurement and both forecasts exist; Missing counts the other slots of the window.
type HomeForecastShadowWindow struct {
	Start, End time.Time
	Issued     time.Time
	Actual     float64
	Variants   map[string]float64
	Slots      int
	Missing    int
}

// ShadowWindowFunc maps an issue time to the window its snapshot is scored on,
// ok false when that issue scores none.
type ShadowWindowFunc func(issued time.Time) (start, end time.Time, ok bool)

// HomeForecastShadowWindows returns, oldest first, one window per horizon
// snapshot that window maps to (the earliest snapshot when several map to the
// same window), for windows that ended before to. Measured energy excludes
// recovered and incomplete slots, as calibrateHomeRanges does.
func HomeForecastShadowWindows(window ShadowWindowFunc, variants []string, to time.Time) ([]HomeForecastShadowWindow, error) {
	if db.Instance == nil {
		return nil, errors.New("database offline")
	}
	type row struct {
		Issued  int64
		Variant string
		Slot    int64
		Base    float64
	}
	var rows []row
	if err := db.Instance.Raw(`SELECT s.issued, s.variant, s.slot, s.base FROM home_forecast_shadow_samples s
		JOIN (`+horizonSnapshots()+`) h ON h.issued = s.issued AND h.variant = s.variant
		WHERE s.variant IN ?`, variants).Scan(&rows).Error; err != nil {
		return nil, err
	}

	byIssue := make(map[int64]map[string]map[int64]float64)
	for _, r := range rows {
		if byIssue[r.Issued] == nil {
			byIssue[r.Issued] = make(map[string]map[int64]float64)
		}
		if byIssue[r.Issued][r.Variant] == nil {
			byIssue[r.Issued][r.Variant] = make(map[int64]float64)
		}
		byIssue[r.Issued][r.Variant][r.Slot] = r.Base
	}
	issues := make([]int64, 0, len(byIssue))
	for issued := range byIssue {
		issues = append(issues, issued)
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i] < issues[j] })

	var res []HomeForecastShadowWindow
	seen := make(map[int64]bool)
	var from, until int64
	for _, issued := range issues {
		start, end, ok := window(time.Unix(issued, 0))
		if !ok || end.After(to) || seen[start.Unix()] {
			continue
		}
		seen[start.Unix()] = true
		res = append(res, HomeForecastShadowWindow{Start: start, End: end, Issued: time.Unix(issued, 0), Variants: make(map[string]float64)})
		if from == 0 || start.Unix() < from {
			from = start.Unix()
		}
		until = max(until, end.Unix())
	}
	if len(res) == 0 {
		return nil, nil
	}

	type measured struct {
		Ts     int64
		Energy float64
	}
	var actual []measured
	if err := db.Instance.Raw(`SELECT ts, energy FROM meters WHERE meter = 1 AND ts >= ? AND ts < ? AND energy IS NOT NULL AND COALESCE(recovered,0)=0 AND COALESCE(incomplete,0)=0`, from, until).Scan(&actual).Error; err != nil {
		return nil, err
	}
	energy := make(map[int64]float64, len(actual))
	for _, m := range actual {
		if finite(m.Energy) && m.Energy >= 0 {
			energy[m.Ts] = m.Energy
		}
	}

	for i := range res {
		w := &res[i]
		forecast := byIssue[w.Issued.Unix()]
	slots:
		for t := w.Start; t.Before(w.End); t = t.Add(tariff.SlotDuration) {
			w.Slots++
			a, ok := energy[t.Unix()]
			if !ok {
				w.Missing++
				continue
			}
			values := make(map[string]float64, len(variants))
			for _, v := range variants {
				f, ok := forecast[v][t.Unix()]
				if !ok {
					w.Missing++
					continue slots
				}
				values[v] = f
			}
			w.Actual += a
			for v, f := range values {
				w.Variants[v] += f
			}
		}
	}
	return res, nil
}

// deleteHomeForecastShadowSamples removes shadow predictions targeting [from, to).
func deleteHomeForecastShadowSamples(from, to time.Time) (int64, error) {
	r := db.Instance.Where("slot >= ? AND slot < ?", from.Unix(), to.Unix()).Delete(new(homeForecastShadowSample))
	return r.RowsAffected, r.Error
}
