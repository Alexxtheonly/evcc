package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/site"
	"github.com/evcc-io/evcc/core/vehicle"
	"github.com/gorilla/mux"
)

// minSocHandler updates min soc
func minSocHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		soc, err := strconv.Atoi(vars["value"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		v.SetMinSoc(soc)

		res := struct {
			Soc int `json:"soc"`
		}{
			Soc: v.GetMinSoc(),
		}

		jsonWrite(w, res)
	}
}

// cheapBandSocHandler updates the cheap-charge band top soc, 0 turns the band off
func cheapBandSocHandler(site site.API) http.HandlerFunc {
	return vehicleIntHandler(site, 0, 100, func(v vehicle.API, value int) int {
		v.SetCheapBandSoc(value)
		return v.GetCheapBandSoc()
	})
}

// cheapBandMinRunHandler updates the minimum charging run inside the band in minutes
func cheapBandMinRunHandler(site site.API) http.HandlerFunc {
	return vehicleIntHandler(site, 0, 240, func(v vehicle.API, value int) int {
		v.SetCheapBandMinRun(value)
		return v.GetCheapBandMinRun()
	})
}

// vehicleIntHandler validates a bounded integer vehicle setting and stores it
func vehicleIntHandler(site site.API, minimum, maximum int, set func(vehicle.API, int) int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		value, err := strconv.Atoi(vars["value"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}
		if value < minimum || value > maximum {
			jsonError(w, http.StatusBadRequest, fmt.Errorf("value must be %d..%d", minimum, maximum))
			return
		}

		jsonWrite(w, struct {
			Value int `json:"value"`
		}{set(v, value)})
	}
}

// limitSocHandler updates limit soc
func limitSocHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		soc, err := strconv.Atoi(vars["value"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		v.SetLimitSoc(soc)

		res := struct {
			Soc int `json:"soc"`
		}{
			Soc: v.GetLimitSoc(),
		}

		jsonWrite(w, res)
	}
}

// vehicleModeHandler updates the vehicle charge mode (empty value clears it)
func vehicleModeHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		mode, err := api.ChargeModeString(vars["value"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		v.SetMode(mode)

		res := struct {
			Mode api.ChargeMode `json:"mode"`
		}{
			Mode: v.GetMode(),
		}

		jsonWrite(w, res)
	}
}

// planSocHandler updates plan soc and time
func planSocHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		ts, err := time.ParseInLocation(time.RFC3339, vars["time"], nil)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		soc, err := strconv.Atoi(vars["value"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		if err := v.SetPlanSoc(ts, soc); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		site.Optimize()

		ts, soc = v.GetPlanSoc()

		res := struct {
			Soc  int       `json:"soc"`
			Time time.Time `json:"time"`
		}{
			Soc:  soc,
			Time: ts,
		}

		jsonWrite(w, res)
	}
}

func planStrategyHandlerSetter(r *http.Request, set func(api.PlanStrategy) error) error {
	var res api.PlanStrategy
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		return err
	}

	return set(res)
}

// updatePlanStrategyHandler updates plan strategy
func updatePlanStrategyHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		if err := planStrategyHandlerSetter(r, v.SetPlanStrategy); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		res := v.GetPlanStrategy()

		jsonWrite(w, res)
	}
}

// addRepeatingPlansHandler handles any information regarding weekday, hour, minute, soc and isActive
func addRepeatingPlansHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		var res []api.RepeatingPlan
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		if err := v.SetRepeatingPlans(res); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		site.Optimize()

		jsonWrite(w, res)
	}
}

// updateAdaptivePlansHandler stores the adaptive (learned) repeating plans
func updateAdaptivePlansHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		var res []api.RepeatingPlan
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		if err := v.SetAdaptivePlans(res); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, res)
	}
}

// removeAdaptivePlansHandler clears the adaptive repeating plans
func removeAdaptivePlansHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		if err := v.SetAdaptivePlans(nil); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, struct{}{})
	}
}

// adaptivePlanLearningHandler enables or disables adaptive plan learning
func adaptivePlanLearningHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		enabled, err := strconv.ParseBool(vars["value"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		if err := v.SetAdaptivePlanLearning(enabled); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		jsonWrite(w, enabled)
	}
}

// planSocRemoveHandler removes plan soc and time
func planSocRemoveHandler(site site.API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		v, err := site.Vehicles().ByName(vars["name"])
		if err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		if err := v.SetPlanSoc(time.Time{}, 0); err != nil {
			jsonError(w, http.StatusBadRequest, err)
			return
		}

		site.Optimize()

		jsonWrite(w, struct{}{})
	}
}
