package tariff

import (
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/samber/lo"
)

type combined struct {
	tariffs []api.Tariff
}

func NewCombined(tariffs []api.Tariff) api.Tariff {
	return &combined{
		tariffs: tariffs,
	}
}

func (t *combined) Rates() (api.Rates, error) {
	var rates api.Rates

	for _, t := range t.tariffs {
		rr, err := t.Rates()
		if err != nil {
			return nil, err
		}

		rates = append(rates, rr...)
	}

	rates.Sort()

	var res api.Rates

	partitions := lo.PartitionBy(rates, func(r api.Rate) time.Time {
		return r.Start
	})

	for _, rr := range partitions {
		r := api.Rate{
			Start: rr[0].Start,
			End:   rr[0].End,
			Value: lo.SumBy(rr, func(r api.Rate) float64 {
				return r.Value
			}),
		}

		r.Low, _ = sumBand(rr, func(r api.Rate) *float64 { return r.Low })
		r.High, _ = sumBand(rr, func(r api.Rate) *float64 { return r.High })

		res = append(res, r)
	}

	return res, nil
}

// sumBand sums a confidence bound (Rate.Low or Rate.High) across a partition of rates
// sharing the same slot, e.g. two solar planes combined by NewCombined. Returns ok=false
// if any rate in the partition is missing the bound: a partial sum across only some of the
// combined tariffs would silently understate (Low) or overstate (High) the combined
// estimate's actual confidence interval, so "no band" is the safe result rather than a
// wrong number - matching how Rate.Low/High already treat nil as "unknown", not zero.
func sumBand(rr api.Rates, get func(api.Rate) *float64) (*float64, bool) {
	sum := 0.0
	for _, r := range rr {
		v := get(r)
		if v == nil {
			return nil, false
		}
		sum += *v
	}
	return &sum, true
}

func (t *combined) Type() api.TariffType {
	return api.TariffTypeSolar
}
