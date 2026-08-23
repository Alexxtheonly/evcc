package metrics

// Savings ledger (ADR-011): the merged per-slot view the whole ledger computes from.
//
// ADR-011 rule 2 (non-negotiable): this file and everything built on top of it reads
// ONLY the meters and tariffs tables. It must never import or reference greenShare,
// effectivePrice, sessions.Price or sessions.PricePerKWh - those are the lineage the
// ledger exists to replace, not to depend on. If a future change needs a number from
// that lineage, that is a sign it belongs somewhere other than the ledger.

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

// ErrBeforeTariffStart is returned when the requested period starts before the
// earliest slot the tariffs table has a price for. ADR-011 honesty rule 4: refuse
// rather than fabricate a price for a period the site has no record of.
type ErrBeforeTariffStart struct {
	Earliest time.Time // zero if the tariffs table has no priced slot at all
}

func (e *ErrBeforeTariffStart) Error() string {
	if e.Earliest.IsZero() {
		return "no priced tariff slots recorded yet"
	}
	return fmt.Sprintf("no tariff data before %s", e.Earliest.Format(time.RFC3339))
}

// EarliestTariffSlot returns the start of the first 15min slot for which both a grid
// and a feed-in price are on record, or the zero time if none is. Both prices are
// required because the ledger needs to cost import and export together - a slot with
// only one of the two can't honestly price either.
func EarliestTariffSlot() (time.Time, error) {
	var ts sql.NullInt64
	if err := db.Instance.Model(new(tariffValue)).
		Where("grid IS NOT NULL AND feedin IS NOT NULL").
		Select("MIN(ts)").Scan(&ts).Error; err != nil {
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return time.Unix(ts.Int64, 0), nil
}

// slotData is one 15min slot's merged view: measured grid/home/pv/battery energy and
// the realised tariff prices. A slot only ends up here if every input the requested
// computation needs was present and neither recovered nor incomplete (see
// buildLedgerSlots) - PVKWh and BatterySocFrac are zero/nil rather than fabricated
// when the site has no PV or no battery configured at all.
type slotData struct {
	Start                  time.Time
	GridImportKWh          float64
	GridExportKWh          float64
	HomeKWh                float64
	PVKWh                  float64
	BatteryChargeKWh       float64
	BatteryDischargeKWh    float64
	BatterySocFrac         *float64 // 0..1, at slot start; nil if no battery configured
	PriceGrid, PriceFeedIn float64
}

// Coverage reports what fraction of a period's slots the ledger could actually
// compute over. ADR-011 honesty rule 3: excluded slots (recovered, incomplete, or
// missing an input entirely) are dropped and reported, never scaled up to compensate.
type Coverage struct {
	ValidSlots int     `json:"validSlots"`
	TotalSlots int     `json:"totalSlots"`
	Fraction   float64 `json:"fraction"`
}

// ledgerSlotSet is the result of buildLedgerSlots: the valid per-slot data plus enough
// bookkeeping (whether PV/battery are configured at all, and the period's total slot
// count) to report coverage honestly.
type ledgerSlotSet struct {
	Slots      []slotData
	TotalSlots int
	HasPV      bool
	HasBattery bool
}

func (s *ledgerSlotSet) coverage() Coverage {
	var frac float64
	if s.TotalSlots > 0 {
		frac = float64(len(s.Slots)) / float64(s.TotalSlots)
	}
	return Coverage{ValidSlots: len(s.Slots), TotalSlots: s.TotalSlots, Fraction: frac}
}

// groupSlotRow is one 15min slot's aggregated meter reading for a single entity
// group (grid, pv, battery, home), summed/averaged across every entity in that group
// so a multi-meter site (e.g. two PV strings) reduces to one series per group.
type groupSlotRow struct {
	Ts           int64
	Energy       float64
	ReturnEnergy float64
	SocFrac      sql.NullFloat64 // AVG(soc_temp), percent; only meaningful for battery
	Excluded     bool            // any contributing row this slot was recovered or incomplete
}

// queryGroupSlots aggregates the meters table by slot for every entity in the given
// group. hasEntities is false when the site has no entity in that group at all
// (e.g. no PV configured) - the caller must treat that as "zero energy, every slot
// valid" rather than "every slot excluded", which is why this is a distinct return
// value instead of an empty map.
func queryGroupSlots(group string, from, to time.Time) (rows map[int64]groupSlotRow, hasEntities bool, err error) {
	var ids []int
	if err := db.Instance.Model(new(entity)).Where(`"group" = ?`, group).Pluck("id", &ids).Error; err != nil {
		return nil, false, err
	}
	if len(ids) == 0 {
		return nil, false, nil
	}

	type row struct {
		Ts           int64
		Energy       float64
		ReturnEnergy float64
		SocFrac      sql.NullFloat64
		Excluded     bool
	}

	var res []row
	if err := db.Instance.Table("meters").
		Select(`ts, COALESCE(SUM(energy), 0) AS energy, COALESCE(SUM(return_energy), 0) AS return_energy,
			AVG(soc_temp) AS soc_frac, MAX(CASE WHEN recovered OR incomplete THEN 1 ELSE 0 END) AS excluded`).
		Where("meter IN ? AND ts >= ? AND ts < ?", ids, from.Unix(), to.Unix()).
		Group("ts").
		Scan(&res).Error; err != nil {
		return nil, true, err
	}

	rows = make(map[int64]groupSlotRow, len(res))
	for _, r := range res {
		rows[r.Ts] = groupSlotRow{Ts: r.Ts, Energy: r.Energy, ReturnEnergy: r.ReturnEnergy, SocFrac: r.SocFrac, Excluded: r.Excluded}
	}
	return rows, true, nil
}

// tariffSlot is one 15min slot's realised grid/feed-in price.
type tariffSlot struct {
	Grid, FeedIn float64
}

// queryTariffSlots returns the slots in [from,to) that have BOTH a grid and a
// feed-in price on record. A slot missing either is simply absent from the result -
// see buildLedgerSlots, which then excludes it from coverage rather than pricing it
// with a fabricated value.
func queryTariffSlots(from, to time.Time) (map[int64]tariffSlot, error) {
	type row struct {
		Ts           int64
		Grid, FeedIn float64
	}
	var res []row
	if err := db.Instance.Model(new(tariffValue)).
		Select("ts, grid, feedin").
		Where("ts >= ? AND ts < ? AND grid IS NOT NULL AND feedin IS NOT NULL", from.Unix(), to.Unix()).
		Scan(&res).Error; err != nil {
		return nil, err
	}

	m := make(map[int64]tariffSlot, len(res))
	for _, r := range res {
		m[r.Ts] = tariffSlot{Grid: r.Grid, FeedIn: r.FeedIn}
	}
	return m, nil
}

// buildLedgerSlots assembles the merged, filtered slot series every ledger
// computation runs on. A slot is included only if the grid meter, the home meter, the
// tariff (both prices), and - when the site has them configured - PV and the battery
// (including its SoC) all have a usable, non-excluded reading for that slot. Anything
// less and the slot is dropped, never interpolated (ADR-011 rules 3 and 5).
//
// from must not precede the earliest priced tariff slot; see ErrBeforeTariffStart.
func buildLedgerSlots(from, to time.Time) (*ledgerSlotSet, error) {
	if !to.After(from) {
		return nil, errors.New("invalid period: to must be after from")
	}

	earliest, err := EarliestTariffSlot()
	if err != nil {
		return nil, err
	}
	if earliest.IsZero() || from.Before(earliest) {
		return nil, &ErrBeforeTariffStart{Earliest: earliest}
	}

	gridRows, hasGrid, err := queryGroupSlots(Grid, from, to)
	if err != nil {
		return nil, err
	}
	if !hasGrid {
		return nil, errors.New("no grid meter configured")
	}

	homeRows, hasHome, err := queryGroupSlots(Home, from, to)
	if err != nil {
		return nil, err
	}
	if !hasHome {
		return nil, errors.New("no home meter configured")
	}

	pvRows, hasPV, err := queryGroupSlots(PV, from, to)
	if err != nil {
		return nil, err
	}

	batRows, hasBattery, err := queryGroupSlots(Battery, from, to)
	if err != nil {
		return nil, err
	}

	tariffRows, err := queryTariffSlots(from, to)
	if err != nil {
		return nil, err
	}

	total := int(to.Sub(from) / tariff.SlotDuration)

	slots := make([]slotData, 0, total)
	for ts := from; ts.Before(to); ts = ts.Add(tariff.SlotDuration) {
		u := ts.Unix()

		g, ok := gridRows[u]
		if !ok || g.Excluded {
			continue
		}
		h, ok := homeRows[u]
		if !ok || h.Excluded {
			continue
		}
		tv, ok := tariffRows[u]
		if !ok {
			continue
		}

		s := slotData{
			Start:         ts,
			GridImportKWh: g.Energy,
			GridExportKWh: g.ReturnEnergy,
			HomeKWh:       h.Energy,
			PriceGrid:     tv.Grid,
			PriceFeedIn:   tv.FeedIn,
		}

		if hasPV {
			p, ok := pvRows[u]
			if !ok || p.Excluded {
				continue
			}
			s.PVKWh = p.Energy
		}

		if hasBattery {
			b, ok := batRows[u]
			if !ok || b.Excluded || !b.SocFrac.Valid {
				continue
			}
			s.BatteryChargeKWh = b.Energy
			s.BatteryDischargeKWh = b.ReturnEnergy
			frac := b.SocFrac.Float64 / 100
			s.BatterySocFrac = &frac
		}

		slots = append(slots, s)
	}

	return &ledgerSlotSet{Slots: slots, TotalSlots: total, HasPV: hasPV, HasBattery: hasBattery}, nil
}
