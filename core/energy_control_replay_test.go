package core

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/require"
)

// TestEnergyCapturedSnapshotReplay re-solves recorded inputs, not counterfactual battery history.
func TestEnergyCapturedSnapshotReplay(t *testing.T) {
	path, url := os.Getenv("EVCC_CONTROL_SNAPSHOTS"), os.Getenv("EVCC_ENERGY_SOLVER_TEST_URL")
	if path == "" || url == "" {
		t.Skip("private snapshots and pinned companion required")
	}
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var rows []struct {
		ID      uint64 `json:"id"`
		TS      int64  `json:"ts"`
		Request string `json:"request"`
		Quality string `json:"quality"`
	}
	require.NoError(t, json.Unmarshal(body, &rows))
	client, err := optimizer.NewClientWithResponses(url)
	require.NoError(t, err)
	type replayRow struct {
		ID        uint64           `json:"id"`
		TS        int64            `json:"ts"`
		Seconds   int              `json:"seconds"`
		Original  *energyScenarios `json:"original"`
		Corrected *energyScenarios `json:"corrected"`
	}
	var replay []replayRow
	for _, row := range rows {
		var req energyRequest
		var quality optimizerInsights
		require.NoError(t, json.Unmarshal([]byte(row.Request), &req))
		require.NoError(t, json.Unmarshal([]byte(row.Quality), &quality))
		if quality.Scenarios == nil || quality.Scenarios.Status != "evaluated" {
			continue
		}
		details := requestDetails{}
		for _, dev := range quality.Devices {
			detail := batteryDetail{Type: batteryType(dev.Kind), Name: dev.Name, Capacity: dev.CapacityKWh, controllable: dev.Kind == "battery"}
			for _, economics := range quality.Economics {
				if economics.Name == dev.Name {
					detail.etaC, detail.etaD = economics.ChargeEfficiency, economics.DischargeEfficiency
					detail.wearPerKWh = economics.WearPerKWh
				}
			}
			details.BatteryDetails = append(details.BatteryDetails, detail)
		}
		site := &Site{log: util.NewLogger("replay"), optimizerClient: client, energyInsights: quality}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		base, err := site.solveEnergy(ctx, req)
		if err != nil {
			cancel()
			t.Fatalf("snapshot %d: %v", row.ID, err)
		}
		_, corrected, err := site.robustEnergy(ctx, req, details, base)
		cancel()
		require.NoError(t, err, "snapshot %d", row.ID)
		replay = append(replay, replayRow{row.ID, row.TS, req.TimeSeries.Dt[0], quality.Scenarios, corrected})
	}
	if out := os.Getenv("EVCC_CONTROL_REPLAY_OUTPUT"); out != "" {
		body, err = json.MarshalIndent(replay, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out, body, 0600))
	}
	t.Logf("Re-solved %d recorded snapshots. Recorded SoC and inputs held fixed; not realized savings or closed-loop replay.", len(replay))
}

// TestEnergyControlCostSweep keeps recorded SoC fixed; gaps between archives are not invented control cycles.
func TestEnergyControlCostSweep(t *testing.T) {
	path, replayPath := os.Getenv("EVCC_CONTROL_SNAPSHOTS"), os.Getenv("EVCC_CONTROL_REPLAY_OUTPUT")
	if path == "" || replayPath == "" {
		t.Skip("private captured inputs and re-solved results required")
	}
	var source []struct {
		ID                       uint64
		Request, Result, Quality string
	}
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &source))
	var replay []struct {
		ID        uint64
		Corrected *energyScenarios
	}
	body, err = os.ReadFile(replayPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &replay))
	for _, fraction := range []float64{0, .0001, .001, .003, .01, .03, .1} {
		prior := "hold"
		switches, chargeTransitions, retained := 0, 0, 0
		var excess float64
		for _, row := range replay {
			index := slices.IndexFunc(source, func(s struct {
				ID                       uint64
				Request, Result, Quality string
			}) bool { return s.ID == row.ID })
			require.GreaterOrEqual(t, index, 0)
			var req energyRequest
			var result optimizer.OptimizationResult
			var quality optimizerInsights
			require.NoError(t, json.Unmarshal([]byte(source[index].Request), &req))
			require.NoError(t, json.Unmarshal([]byte(source[index].Result), &result))
			require.NoError(t, json.Unmarshal([]byte(source[index].Quality), &quality))
			details := requestDetails{}
			for _, dev := range quality.Devices {
				details.BatteryDetails = append(details.BatteryDetails, batteryDetail{Type: batteryType(dev.Kind)})
			}
			all, err := executableFirstActions(req, details, result)
			require.NoError(t, err)
			var actions []energyFirstAction
			var costs [][]float64
			for _, eval := range row.Corrected.Evaluations {
				i := slices.IndexFunc(all, func(a energyFirstAction) bool { return a.name == eval.Action })
				require.GreaterOrEqual(t, i, 0)
				actions, costs = append(actions, all[i]), append(costs, eval.Costs)
			}
			best, info := selectEnergyAction(req, actions, costs, "")
			require.GreaterOrEqual(t, best, 0)
			selected := best
			p := slices.IndexFunc(actions, func(a energyFirstAction) bool { return a.name == prior })
			if p >= 0 {
				gap := info.Evaluations[p].Regret - info.Evaluations[best].Regret
				margin := energyActionMateriality(req, actions[p], actions[best]) / energySwitchingFraction * fraction
				if gap <= margin {
					selected = p
					excess = max(excess, gap)
					if selected != best {
						retained++
					}
				}
			}
			mode := actions[selected].name
			if mode != prior {
				switches++
			}
			if (mode == "charge") != (prior == "charge") {
				chargeTransitions++
			}
			prior = mode
		}
		t.Logf("fraction=%g sampledTransitions=%d chargeTransitions=%d retained=%d maxAdditionalRegret=%0.9f currency; fixed recorded SoC, no bill-savings claim", fraction, switches, chargeTransitions, retained, excess)
	}
}
