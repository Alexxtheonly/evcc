package vehicle

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/server/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAdapter(name string) *adapter {
	return &adapter{log: util.NewLogger("foo"), name: name}
}

func repeatingPlan(soc int) api.RepeatingPlan {
	return api.RepeatingPlan{Weekdays: []int{1, 2, 3, 4, 5}, Time: "06:30", Tz: "Europe/Berlin", Soc: soc, Active: true}
}

// backdate rewrites the stored adaptive plan timestamp
func backdateAdaptivePlans(t *testing.T, v *adapter, age time.Duration) {
	var store adaptivePlanStore
	require.NoError(t, settings.Json(v.key()+keys.AdaptivePlans, &store))
	store.Updated = time.Now().Add(-age)
	require.NoError(t, settings.SetJson(v.key()+keys.AdaptivePlans, store))
}

func TestAdaptivePlanStore(t *testing.T) {
	v := testAdapter("store")

	plans := []api.RepeatingPlan{repeatingPlan(40)}
	require.NoError(t, v.SetAdaptivePlans(plans))

	res, updated := v.GetAdaptivePlans()
	assert.Equal(t, plans, res)
	assert.WithinDuration(t, time.Now(), updated, time.Minute, "timestamp is server-stamped")

	// clearing leaves user plans untouched
	require.NoError(t, v.SetRepeatingPlans([]api.RepeatingPlan{repeatingPlan(70)}))
	require.NoError(t, v.SetAdaptivePlans(nil))

	res, _ = v.GetAdaptivePlans()
	assert.Empty(t, res)
	assert.Len(t, v.GetRepeatingPlans(), 1)
}

func TestAdaptivePlanValidation(t *testing.T) {
	v := testAdapter("validation")

	for _, tc := range []struct {
		name string
		plan api.RepeatingPlan
	}{
		{"weekday out of range", api.RepeatingPlan{Weekdays: []int{7}, Time: "06:30", Tz: "Europe/Berlin", Soc: 40}},
		{"invalid timezone", api.RepeatingPlan{Weekdays: []int{1}, Time: "06:30", Tz: "Mars/Olympus", Soc: 40}},
		{"invalid time", api.RepeatingPlan{Weekdays: []int{1}, Time: "25:99", Tz: "Europe/Berlin", Soc: 40}},
		{"no weekdays", api.RepeatingPlan{Weekdays: nil, Time: "06:30", Tz: "Europe/Berlin", Soc: 40}},
		{"soc too low", api.RepeatingPlan{Weekdays: []int{1}, Time: "06:30", Tz: "Europe/Berlin", Soc: 0}},
		{"soc too high", api.RepeatingPlan{Weekdays: []int{1}, Time: "06:30", Tz: "Europe/Berlin", Soc: 101}},
	} {
		assert.Error(t, v.SetAdaptivePlans([]api.RepeatingPlan{tc.plan}), tc.name)
	}

	// more than one plan per weekday is pointless
	tooMany := make([]api.RepeatingPlan, 8)
	for i := range tooMany {
		tooMany[i] = repeatingPlan(40)
	}
	assert.Error(t, v.SetAdaptivePlans(tooMany), "too many plans")
}

func TestEffectiveRepeatingPlans(t *testing.T) {
	adaptive := []api.RepeatingPlan{repeatingPlan(45)}
	user := []api.RepeatingPlan{repeatingPlan(70)}

	inactive := repeatingPlan(70)
	inactive.Active = false

	// no plans at all
	v := testAdapter("eff-none")
	assert.Empty(t, v.GetEffectiveRepeatingPlans())

	// fresh adaptive plans drive charging
	v = testAdapter("eff-adaptive")
	require.NoError(t, v.SetAdaptivePlans(adaptive))
	assert.Equal(t, adaptive, v.GetEffectiveRepeatingPlans())

	// stored user plans win absolutely
	v = testAdapter("eff-user")
	require.NoError(t, v.SetAdaptivePlans(adaptive))
	require.NoError(t, v.SetRepeatingPlans(user))
	assert.Equal(t, user, v.GetEffectiveRepeatingPlans())

	// all-inactive user plans mean "no repeating charging" — adaptive must not
	// resurrect them
	v = testAdapter("eff-inactive")
	require.NoError(t, v.SetAdaptivePlans(adaptive))
	require.NoError(t, v.SetRepeatingPlans([]api.RepeatingPlan{inactive}))
	assert.Equal(t, []api.RepeatingPlan{inactive}, v.GetEffectiveRepeatingPlans())

	// a user who deleted their last plan re-enables adaptive
	v = testAdapter("eff-deleted")
	require.NoError(t, v.SetAdaptivePlans(adaptive))
	require.NoError(t, v.SetRepeatingPlans([]api.RepeatingPlan{}))
	assert.Equal(t, adaptive, v.GetEffectiveRepeatingPlans())

	// stale adaptive plans are ignored: a dead learner must not steer charging
	// on old patterns
	v = testAdapter("eff-stale")
	require.NoError(t, v.SetAdaptivePlans(adaptive))
	backdateAdaptivePlans(t, v, AdaptivePlansValidity+time.Hour)
	assert.Empty(t, v.GetEffectiveRepeatingPlans())

	// just inside the validity window
	v = testAdapter("eff-fresh")
	require.NoError(t, v.SetAdaptivePlans(adaptive))
	backdateAdaptivePlans(t, v, AdaptivePlansValidity-time.Hour)
	assert.Equal(t, adaptive, v.GetEffectiveRepeatingPlans())

	// the vehicle's soc limit caps learned targets: an active plan bypasses the
	// loadpoint's LimitSocReached check
	v = testAdapter("eff-limit")
	require.NoError(t, v.SetAdaptivePlans([]api.RepeatingPlan{repeatingPlan(80)}))
	v.SetLimitSoc(60)
	res := v.GetEffectiveRepeatingPlans()
	require.Len(t, res, 1)
	assert.Equal(t, 60, res[0].Soc)

	// clamping must not modify the stored plans
	stored, _ := v.GetAdaptivePlans()
	require.Len(t, stored, 1)
	assert.Equal(t, 80, stored[0].Soc)
}
