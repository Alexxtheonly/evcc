package tariff

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
)

// Merged combines a primary tariff with a secondary (forecast) tariff.
// Primary rates are used where available, secondary fills gaps after primary ends.
type Merged struct {
	log       *util.Logger
	primary   api.Tariff
	secondary api.Tariff
}

func init() {
	registry.AddCtx("merged", NewMergedFromConfig)
}

func NewMergedFromConfig(ctx context.Context, other map[string]any) (api.Tariff, error) {
	var cc struct {
		Primary, Secondary Typed
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	primary, err := NewFromConfig(ctx, cc.Primary.Type, cc.Primary.Other)
	if err != nil {
		return nil, err
	}

	secondary, err := NewFromConfig(ctx, cc.Secondary.Type, cc.Secondary.Other)
	if err != nil {
		return nil, err
	}

	if pType, sType := primary.Type(), secondary.Type(); pType != sType {
		return nil, fmt.Errorf("primary and secondary tariff types are not compatible: %v vs %v", pType, sType)
	}

	t := &Merged{
		log:       util.NewLogger("merged"),
		primary:   primary,
		secondary: secondary,
	}

	return t, nil
}

// markForecast returns a clone of rates with Forecast set, so the caller's own cached
// slice (e.g. the secondary tariff's) is never mutated in place.
func markForecast(rates api.Rates) api.Rates {
	marked := slices.Clone(rates)
	for i := range marked {
		marked[i].Forecast = true
	}
	return marked
}

// Rates implements the api.Tariff interface
func (t *Merged) Rates() (api.Rates, error) {
	result, err := t.primary.Rates()
	if err != nil {
		// primary failed entirely, so every returned rate is a secondary prediction
		t.log.DEBUG.Printf("primary tariff failed, falling back to secondary: %v", err)
		secondaryRates, err := t.secondary.Rates()
		if err != nil {
			return nil, err
		}
		return markForecast(secondaryRates), nil
	}

	secondaryRates, err := t.secondary.Rates()
	if err != nil {
		t.log.DEBUG.Printf("secondary tariff failed, using primary only: %v", err)
		return result, nil
	}

	// If primary is empty, every returned rate is again a secondary prediction
	if len(result) == 0 {
		return markForecast(secondaryRates), nil
	}

	// Find where primary data ends and append secondary rates starting there
	primaryEnd := result[len(result)-1].End
	if idx, found := slices.BinarySearchFunc(secondaryRates, primaryEnd, func(r api.Rate, t time.Time) int {
		return r.Start.Compare(t)
	}); found {
		// mark the gap-filled tail as forecast so consumers can tell a settled price
		// from a prediction beyond the primary tariff's known horizon
		return append(result, markForecast(secondaryRates[idx:])...), nil
	}

	t.log.WARN.Printf("secondary tariff does not align gapless with primary, ignoring secondary")
	return result, nil
}

// Type implements the api.Tariff interface
func (t *Merged) Type() api.TariffType {
	return t.primary.Type()
}
