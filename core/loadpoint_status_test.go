package core

import (
	"testing"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/session"
	serverdb "github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestStatusEvents(t *testing.T) {
	tc := []struct {
		from, to api.ChargeStatus
		events   []string
	}{
		{api.StatusNone, api.StatusA, []string{evVehicleDisconnect}},
		{api.StatusNone, api.StatusB, []string{evVehicleConnect}},
		{api.StatusNone, api.StatusC, []string{evVehicleConnect, evChargeStart}},

		{api.StatusA, api.StatusB, []string{evVehicleConnect}},
		{api.StatusA, api.StatusC, []string{evVehicleConnect, evChargeStart}},

		{api.StatusB, api.StatusA, []string{evVehicleDisconnect}},
		{api.StatusB, api.StatusC, []string{evChargeStart}},

		{api.StatusC, api.StatusA, []string{evChargeStop, evVehicleDisconnect}},
		{api.StatusC, api.StatusB, []string{evChargeStop}},
	}

	for _, tc := range tc {
		ev := statusEvents(tc.from, tc.to)
		assert.Equalf(t, tc.events, ev, "from %s to %s got: %v", tc.from, tc.to, ev)
	}
}

// TestConnectAtBootDoesNotFabricateConnectedTime: statusEvents
// fires evVehicleConnect on the first poll after startup with a car already
// plugged in (prevStatus == api.StatusNone), same as a real connect - only
// the notification is suppressed for that case (see updateChargerStatus),
// not the event itself. evVehicleConnectHandler must not stamp
// session.Connected with evcc's start time in that case: the real plug-in
// time isn't known, so it must be left unset rather than fabricated as
// indistinguishable from a genuine connect.
func TestConnectAtBootDoesNotFabricateConnectedTime(t *testing.T) {
	var err error
	serverdb.Instance, err = serverdb.New("sqlite", ":memory:")
	require.NoError(t, err)

	db, err := session.NewStore("foo", serverdb.Instance)
	require.NoError(t, err)

	clk := clock.NewMock()

	ctrl := gomock.NewController(t)
	mm := api.NewMockMeter(ctrl)
	me := api.NewMockMeterEnergy(ctrl)

	type EnergyDecorator struct {
		api.Meter
		api.MeterEnergy
	}
	cm := &EnergyDecorator{Meter: mm, MeterEnergy: me}

	lp := &Loadpoint{
		log:         util.NewLogger("foo"),
		clock:       clk,
		db:          db,
		chargeMeter: cm,
	}

	// simulates updateChargerStatus having set connectAtBoot on the
	// startup-with-car-plugged-in transition
	lp.connectAtBoot = true
	me.EXPECT().TotalEnergy().Return(1.0, nil)
	lp.evVehicleConnectHandler()

	require.NotNil(t, lp.session)
	assert.Nil(t, lp.session.Connected, "plug-in time is not known after a restart")
	assert.False(t, lp.connectAtBoot, "the flag is consumed, not left set for the next connect")

	// a genuine connect (e.g. after the boot-detected car later disconnects
	// and a different one plugs in) does stamp it
	lp.session = nil
	me.EXPECT().TotalEnergy().Return(1.0, nil)
	lp.evVehicleConnectHandler()

	require.NotNil(t, lp.session)
	require.NotNil(t, lp.session.Connected)
	assert.Equal(t, clk.Now(), *lp.session.Connected)
}
