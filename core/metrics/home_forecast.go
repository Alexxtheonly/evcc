package metrics

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProfileQuality describes observed coverage separately from imputed values.
type ProfileQuality struct {
	Source              string     `json:"source"`
	Samples             int        `json:"samples"`
	RejectedSamples     int        `json:"rejectedSamples"`
	CoveredBuckets      int        `json:"coveredBuckets"`
	InterpolatedBuckets int        `json:"interpolatedBuckets"`
	PooledBuckets       int        `json:"pooledBuckets"`
	Coverage            float64    `json:"coverage"`
	LastGood            *time.Time `json:"lastGood,omitempty"`
	LastGoodAgeSeconds  *float64   `json:"lastGoodAgeSeconds,omitempty"`
	LatestSample        *time.Time `json:"latestSample,omitempty"`
	MissingBuckets      []int      `json:"missingBuckets"`
	Reason              string     `json:"reason,omitempty"`
	RangeSource         string     `json:"rangeSource"`
	CalibrationSamples  int        `json:"calibrationSamples"`
}

// HomeForecastSlot is an absolute fifteen-minute interval with energy in Wh.
type HomeForecastSlot struct {
	Start time.Time `json:"start"`
	Low   float64   `json:"low"`
	Base  float64   `json:"base"`
	High  float64   `json:"high"`
}

// HomeForecastResult includes the evidence behind a household-only prediction.
type HomeForecastResult struct {
	Rates   []HomeForecastSlot `json:"rates"`
	Quality ProfileQuality     `json:"quality"`
}

type profileBucket struct {
	sum, weight float64
	values      []float64
}

func (b *profileBucket) add(v, w float64) {
	b.sum += v * w
	b.weight += w
	b.values = append(b.values, v)
}
func (b profileBucket) forecast() HomeForecastSlot {
	base := b.sum / b.weight * 1e3
	return HomeForecastSlot{Base: base, Low: min(base, Percentile(b.values, .1)*1e3), High: max(base, Percentile(b.values, .9)*1e3)}
}

func profileIndex(t time.Time) int { return t.Hour()*4 + t.Minute()/15 }
func dayType(t time.Time) int {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return 1
	}
	return 0
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func buildHomeProfile(rows []meter, at time.Time) ([2][96]HomeForecastSlot, ProfileQuality, error) {
	var typed [2][96]profileBucket
	var pooled [96]profileBucket
	var out [2][96]HomeForecastSlot
	q := ProfileQuality{Source: "day_type", RangeSource: "historical_variation", MissingBuckets: []int{}}
	for _, r := range rows {
		t := time.Unix(r.Timestamp, 0).In(at.Location())
		if t.Add(tariff.SlotDuration).After(at) {
			continue
		}
		if r.Recovered || r.Incomplete || !finite(r.Energy) || r.Energy < 0 {
			q.RejectedSamples++
			continue
		}
		q.Samples++
		if q.LatestSample == nil || t.After(*q.LatestSample) {
			v := t
			q.LatestSample = &v
		}
		w := math.Exp2(-at.Sub(t).Hours() / (14 * 24))
		i := profileIndex(t)
		pooled[i].add(r.Energy, w)
		typed[dayType(t)][i].add(r.Energy, w)
	}
	for i, b := range pooled {
		if len(b.values) > 0 {
			q.CoveredBuckets++
		} else {
			q.MissingBuckets = append(q.MissingBuckets, i)
		}
	}
	q.Coverage = float64(q.CoveredBuckets) / 96
	if q.Samples < energyProfileMinSamples || q.CoveredBuckets < 92 || q.LatestSample == nil || at.Sub(*q.LatestSample) > 48*time.Hour {
		q.Source = "unavailable"
		q.Reason = fmt.Sprintf("%d clean samples, %d/96 buckets; requires 144 samples, 92 buckets and a reading within 48h", q.Samples, q.CoveredBuckets)
		return out, q, fmt.Errorf("%w: %s", ErrIncomplete, q.Reason)
	}
	for d := range out {
		for i := range out[d] {
			b := typed[d][i]
			if len(b.values) < 2 {
				b = pooled[i]
				if len(b.values) > 0 {
					q.PooledBuckets++
				}
			}
			if len(b.values) > 0 {
				out[d][i] = b.forecast()
				continue
			}
			left, right := pooled[(i+95)%96], pooled[(i+1)%96]
			if l, r := typed[d][(i+95)%96], typed[d][(i+1)%96]; len(l.values) >= 2 && len(r.values) >= 2 {
				left, right = l, r
			}
			if len(left.values) == 0 || len(right.values) == 0 {
				q.Source = "unavailable"
				q.Reason = "missing adjacent time buckets cannot be interpolated"
				return out, q, fmt.Errorf("%w: %s", ErrIncomplete, q.Reason)
			}
			l, r := left.forecast(), right.forecast()
			out[d][i] = HomeForecastSlot{Base: (l.Base + r.Base) / 2, Low: min(l.Low, r.Low), High: max(l.High, r.High)}
			q.InterpolatedBuckets++
		}
	}
	if q.PooledBuckets > 0 {
		q.Source = "pooled"
	}
	if q.InterpolatedBuckets > 0 {
		q.Source = "interpolated"
	}
	return out, q, nil
}

// HomeForecast predicts up to seven days from strictly earlier, clean household data.
func HomeForecast(from, to time.Time) (*HomeForecastResult, error) {
	if from.IsZero() || !to.After(from) || to.Sub(from) > 7*24*time.Hour {
		return nil, fmt.Errorf("invalid household forecast horizon")
	}
	var rows []meter
	if err := db.Instance.Select("meter, ts, COALESCE(energy,-1) AS energy, recovered, incomplete").Where("meter = ? AND ts >= ? AND ts < ?", 1, from.AddDate(0, 0, -37).Unix(), from.Unix()).Order("ts").Find(&rows).Error; err != nil {
		return nil, err
	}
	cut := from.AddDate(0, 0, -30).Unix()
	var current []meter
	for _, r := range rows {
		if r.Timestamp >= cut {
			current = append(current, r)
		}
	}
	profile, q, err := buildHomeProfile(current, from)
	// Reconstruct a last validated profile from history, bounded to seven days.
	if err != nil {
		for days := 1; days <= 7; days++ {
			at := from.AddDate(0, 0, -days)
			var historical []meter
			for _, r := range rows {
				if r.Timestamp >= at.AddDate(0, 0, -30).Unix() && r.Timestamp < at.Unix() {
					historical = append(historical, r)
				}
			}
			candidate, _, e := buildHomeProfile(historical, at)
			if e != nil {
				continue
			}
			profile = candidate
			q.Source = "cached"
			q.LastGood = &at
			age := from.Sub(at).Seconds()
			q.LastGoodAgeSeconds = &age
			err = nil
			break
		}
	} else {
		at := from
		q.LastGood = &at
		age := 0.
		q.LastGoodAgeSeconds = &age
	}
	res := &HomeForecastResult{Quality: q, Rates: []HomeForecastSlot{}}
	if err != nil {
		return res, err
	}
	for t := from.Truncate(tariff.SlotDuration); t.Before(to); t = t.Add(tariff.SlotDuration) {
		v := profile[dayType(t)][profileIndex(t)]
		v.Start = t
		res.Rates = append(res.Rates, v)
	}
	if err := calibrateHomeRanges(from, res); err != nil {
		return res, err
	}
	return res, nil
}

type homeForecastSample struct {
	Slot        int64 `gorm:"uniqueIndex:home_forecast_slot_lead"`
	LeadMinutes int   `gorm:"uniqueIndex:home_forecast_slot_lead"`
	Issued      int64
	Base        float64
	Low         float64
	High        float64
}

// ArchiveHomeForecast freezes issued predictions before their observations exist.
func ArchiveHomeForecast(issued time.Time, slots []HomeForecastSlot) error {
	var samples []homeForecastSample
	for _, lead := range ForecastLeadTimes {
		target := issued.Add(lead).Truncate(tariff.SlotDuration)
		for _, s := range slots {
			if s.Start.Equal(target) && s.Start.After(issued) {
				if !finite(s.Base) || !finite(s.Low) || !finite(s.High) || s.Low < 0 || s.Low > s.Base || s.Base > s.High {
					return fmt.Errorf("invalid household forecast sample")
				}
				samples = append(samples, homeForecastSample{Slot: target.Unix(), LeadMinutes: int(lead.Minutes()), Issued: issued.Unix(), Base: s.Base / 1e3, Low: s.Low / 1e3, High: s.High / 1e3})
				break
			}
		}
	}
	if len(samples) > 0 {
		if err := db.Instance.Clauses(clause.OnConflict{DoNothing: true}).Create(&samples).Error; err != nil {
			return err
		}
	}
	return db.Instance.Where("slot < ?", issued.AddDate(0, 0, -90).Unix()).Delete(new(homeForecastSample)).Error
}

func calibrateHomeRanges(at time.Time, res *HomeForecastResult) error {
	type residual struct {
		LeadMinutes int
		Error       float64
	}
	var rows []residual
	err := db.Instance.Table("home_forecast_samples f").Select("f.lead_minutes, (m.energy - f.base) * 1000 AS error").Joins("JOIN meters m ON m.ts = f.slot AND m.meter = 1").Where("f.slot >= ? AND f.slot < ? AND f.issued < f.slot AND COALESCE(m.recovered,0)=0 AND COALESCE(m.incomplete,0)=0", at.AddDate(0, 0, -90).Unix(), at.Truncate(tariff.SlotDuration).Unix()).Scan(&rows).Error
	if err != nil {
		return err
	}
	byLead := map[int][]float64{}
	for _, r := range rows {
		if finite(r.Error) {
			byLead[r.LeadMinutes] = append(byLead[r.LeadMinutes], r.Error)
		}
	}
	for i, s := range res.Rates {
		lead := nearestLead(s.Start.Sub(at))
		errors := byLead[lead]
		if len(errors) < 30 {
			continue
		}
		res.Rates[i].Low = max(0, min(s.Base, s.Base+Percentile(errors, .1)))
		res.Rates[i].High = max(s.Base, s.Base+Percentile(errors, .9))
		res.Quality.RangeSource = "forecast_errors"
	}
	res.Quality.CalibrationSamples = len(rows)
	return nil
}

func nearestLead(d time.Duration) int {
	best := ForecastLeadTimes[0]
	for _, v := range ForecastLeadTimes {
		if math.Abs(float64(v-d)) < math.Abs(float64(best-d)) {
			best = v
		}
	}
	return int(best.Minutes())
}

// SolarForecastRange returns a measured-error envelope or an explicit uncalibrated spread.
func SolarForecastRange(from time.Time, leadMinutes int, baseWh float64) (low, high float64, samples int, err error) {
	if !finite(baseWh) || baseWh < 0 {
		return 0, 0, 0, fmt.Errorf("invalid solar forecast energy")
	}
	residuals, err := solarResiduals(from, nearestLead(time.Duration(leadMinutes)*time.Minute))
	if err != nil {
		return 0, 0, 0, err
	}
	if len(residuals) < 30 {
		return baseWh * .5, baseWh * 1.5, len(residuals), nil
	}
	return max(0, min(baseWh, baseWh+Percentile(residuals, .1))), max(baseWh, baseWh+Percentile(residuals, .9)), len(residuals), nil
}

var solarResidualCache struct {
	sync.Mutex
	database *gorm.DB
	cutoff   int64
	byLead   map[int][]float64
}

func solarResiduals(from time.Time, lead int) ([]float64, error) {
	solarResidualCache.Lock()
	defer solarResidualCache.Unlock()
	cutoff := from.Truncate(tariff.SlotDuration).Unix()
	if solarResidualCache.database != db.Instance || solarResidualCache.cutoff != cutoff {
		rows, err := QueryLeadTimeSamples(from.AddDate(0, 0, -90))
		if err != nil {
			return nil, err
		}
		byLead := make(map[int][]float64)
		for _, r := range rows {
			if r.Slot < cutoff && finite(r.Actual) && finite(r.Forecast) {
				byLead[r.LeadMinutes] = append(byLead[r.LeadMinutes], (r.Actual-r.Forecast)*1e3)
			}
		}
		solarResidualCache.database = db.Instance
		solarResidualCache.cutoff = cutoff
		solarResidualCache.byLead = byLead
	}
	return solarResidualCache.byLead[lead], nil
}
