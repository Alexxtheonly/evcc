package tariff

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/tariff/solcast"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/request"
	"github.com/evcc-io/evcc/util/transport"
)

type Solcast struct {
	*request.Helper
	log    *util.Logger
	site   string
	fromTo FromTo
	data   *util.Monitor[api.Rates]
}

var _ api.Tariff = (*Solcast)(nil)

func init() {
	registry.Add("solcast", NewSolcastFromConfig)
}

func NewSolcastFromConfig(other map[string]any) (api.Tariff, error) {
	cc := struct {
		Site     string
		Token    string
		Interval time.Duration
		FromTo   `mapstructure:",squash"`
	}{
		Interval: 3 * time.Hour,
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	if cc.Site == "" {
		return nil, errors.New("missing site id")
	}

	if cc.Token == "" {
		return nil, errors.New("missing token")
	}

	log := util.NewLogger("solcast").Redact(cc.Token)

	t := &Solcast{
		log:    log,
		site:   cc.Site,
		Helper: request.NewHelper(log),
		fromTo: cc.FromTo,
		data:   util.NewMonitor[api.Rates](2 * cc.Interval),
	}

	t.Client.Transport = transport.BearerAuth(cc.Token, t.Client.Transport)

	done := make(chan error)
	go t.run(cc.Interval, done)

	if err := <-done; err != nil {
		return nil, err
	}

	return t, nil
}

func (t *Solcast) run(interval time.Duration, done chan error) {
	var once sync.Once

	for ; true; <-time.Tick(interval) {
		// ensure we don't run when not needed, but execute once at startup
		select {
		case <-t.data.Done():
			if !t.fromTo.IsActive(time.Now().Hour()) {
				// keep cached forecast alive while fetching is paused
				mergeRatesAfter(t.data, nil, beginningOfDay())
				continue
			}
		default:
		}

		var res solcast.Forecasts

		if err := backoff.Retry(func() error {
			uri := fmt.Sprintf("https://api.solcast.com.au/rooftop_sites/%s/forecasts?period=PT30M&format=json&hours=96", t.site)
			return backoffPermanentError(t.GetJSON(uri, &res))
		}, bo()); err != nil {
			if reportError(&once, done, err) {
				return
			}
			t.log.ERROR.Println(err)
			continue
		}

		once.Do(func() { close(done) })

		mergeRatesAfter(t.data, solcastRates(res.Forecasts, t.log), beginningOfDay())
		once.Do(func() { close(done) })
	}
}

// solcastRates converts Solcast's forecast periods to rates, preserving their native
// resolution (e.g. 30 minutes) instead of collapsing them to hourly buckets. SlotWrapper
// takes care of expanding sub-hourly slots to the common 15-minute grid.
//
// Period is load-bearing: Start is derived from PeriodEnd minus Period, so a zero or
// negative Period collapses that entry to a zero-length or inverted rate. SlotWrapper
// would then turn it into a single stray 15-minute slot at PeriodEnd, silently dropping
// the rest of that entry's actual horizon and shifting every following entry's apparent
// timing. An entry missing "period" in the response unmarshals to a zero Duration (Go
// skips UnmarshalJSON for an absent key), so this cannot be assumed always-set even
// though the field is always present in Solcast's documented response shape - entries
// with an unusable Period are dropped rather than fed to SlotWrapper.
func solcastRates(forecasts []solcast.Forecast, log *util.Logger) api.Rates {
	data := make(api.Rates, 0, len(forecasts))

	for _, r := range forecasts {
		if r.Period.Duration() <= 0 {
			log.WARN.Printf("solcast: dropping forecast entry with unusable period %v at %v", r.Period.Duration(), r.PeriodEnd)
			continue
		}

		end := r.PeriodEnd.Local()
		data = append(data, api.Rate{
			Start: end.Add(-r.Period.Duration()),
			End:   end,
			Value: r.PvEstimate * 1e3,
			Low:   solcastEstimate(r.PvEstimate10),
			High:  solcastEstimate(r.PvEstimate90),
		})
	}

	return data
}

// solcastEstimate converts a Solcast percentile estimate (kW, possibly absent) to the
// same W unit as Rate.Value, preserving "absent from the response" as nil rather than
// defaulting it to zero.
func solcastEstimate(kw *float64) *float64 {
	if kw == nil {
		return nil
	}
	w := *kw * 1e3
	return &w
}

// Rates implements the api.Tariff interface
func (t *Solcast) Rates() (api.Rates, error) {
	var res api.Rates
	err := t.data.GetFunc(func(val api.Rates) {
		res = slices.Clone(val)
	})
	return res, err
}

// Type implements the api.Tariff interface
func (t *Solcast) Type() api.TariffType {
	return api.TariffTypeSolar
}
