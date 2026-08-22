package tariff

import (
	"errors"
	"testing"
	"time"

	"github.com/evcc-io/evcc/meter/tibber"
	"github.com/evcc-io/evcc/tariff/awattar"
	"github.com/evcc-io/evcc/tariff/elering"
	"github.com/evcc-io/evcc/tariff/entsoe"
	"github.com/evcc-io/evcc/tariff/smartenergy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errFormula simulates a custom formula that fails deterministically once real market data
// flows through it - e.g. a division that only zeroes out for a specific price, which the
// single price=0 smoke test in embed.init() would not have caught.
var errFormula = errors.New("formula error")

func failingCalc(price, charges float64, ts time.Time) (float64, error) {
	return 0, errFormula
}

// TestProviderRatesPropagatesFormulaError asserts that each provider's per-item rate
// conversion returns the formula error to its caller instead of swallowing it - the
// bug fixed for awattar/elering/entsoe/smartenergy/stekker/edf-tempo/tibber, where the
// only prior handling was a log line and a silent continue. Every run() loop now routes
// this error through reportError (see TestRunOrErrorStopsGoroutineOnFormulaError below)
// so it must actually reach run(), not vanish inside the conversion helper.
func TestProviderRatesPropagatesFormulaError(t *testing.T) {
	e := &embed{calc: failingCalc}

	t.Run("awattar", func(t *testing.T) {
		tf := &Awattar{embed: e}
		res, err := tf.rates([]awattar.PriceInfo{{StartTimestamp: time.Now(), EndTimestamp: time.Now()}})
		require.ErrorIs(t, err, errFormula)
		assert.Nil(t, res)
	})

	t.Run("elering", func(t *testing.T) {
		tf := &Elering{embed: e}
		res, err := tf.rates([]elering.Price{{Timestamp: time.Now().Unix()}})
		require.ErrorIs(t, err, errFormula)
		assert.Nil(t, res)
	})

	t.Run("entsoe", func(t *testing.T) {
		tf := &Entsoe{embed: e}
		res, err := tf.rates([]entsoe.Rate{{Start: time.Now(), End: time.Now().Add(time.Hour)}})
		require.ErrorIs(t, err, errFormula)
		assert.Nil(t, res)
	})

	t.Run("smartenergy", func(t *testing.T) {
		tf := &SmartEnergy{embed: e}
		res, err := tf.rates([]smartenergy.Price{{Date: time.Now().Truncate(time.Hour)}})
		require.ErrorIs(t, err, errFormula)
		assert.Nil(t, res)
	})

	t.Run("stekker", func(t *testing.T) {
		tf := &Stekker{embed: e, interval: time.Hour}
		data := []map[string]any{{
			"name": "Market",
			"x":    []any{time.Now().Format(time.RFC3339)},
			"y":    []any{100.0},
		}}
		res, err := tf.rates(data)
		require.ErrorIs(t, err, errFormula)
		assert.Nil(t, res)
	})

	t.Run("edf-tempo", func(t *testing.T) {
		tf := &EdfTempo{embed: e, prices: map[string]float64{"blue": 1}}
		res, err := tf.rates([]edfTempoValue{{
			StartDate: time.Now(),
			EndDate:   time.Now().Add(time.Hour),
			Value:     "blue",
		}})
		require.ErrorIs(t, err, errFormula)
		assert.Nil(t, res)
	})

	t.Run("tibber", func(t *testing.T) {
		ee := &embed{calc: failingCalc, Formula: "x"} // non-empty Formula forces the calc path
		tf := &Tibber{embed: ee}
		res, err := tf.rates([]tibber.Price{{StartsAt: time.Now()}})
		require.ErrorIs(t, err, errFormula)
		assert.Nil(t, res)
	})
}
