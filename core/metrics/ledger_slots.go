package metrics

// Savings ledger (ADR-011): the merged per-slot view the whole ledger computes from.
//
// ADR-011 rule 2 (non-negotiable): this file and everything built on top of it reads
// ONLY the meters and tariffs tables. It must never import or reference greenShare,
// effectivePrice, sessions.Price or sessions.PricePerKWh - those are the lineage the
// ledger exists to replace, not to depend on. If a future change needs a number from
// that lineage, that is a sign it belongs somewhere other than the ledger.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"github.com/evcc-io/evcc/tariff"
)

// MaxLedgerRangeDays bounds a single request's [from,to) window. The ledger endpoint is
// unauthenticated (like its /history/energy and /tariff neighbours - see
// server/http_savings_ledger_handler.go), and ComputeLedger runs upwards of a dozen
// queries plus a battery-history scan per request against a database with a single
// connection (server/db/db.go's SetMaxOpenConns(1)) - an unbounded ?from=2000-01-01
// would queue every other write behind it. 400 days covers "a year plus slack" for any
// legitimate dashboard query.
const MaxLedgerRangeDays = 400

// ErrLedgerRangeTooLarge means the requested [from,to) window exceeds MaxLedgerRangeDays.
var ErrLedgerRangeTooLarge = fmt.Errorf("requested range exceeds the %d-day maximum", MaxLedgerRangeDays)

// ErrLedgerRangeUnaligned means from or to isn't truncated to a tariff.SlotDuration
// boundary. meters.ts and tariffs.ts are always slot starts, and buildLedgerSlots
// walks from `from` in fixed 15-minute steps (see the loop below) - an unaligned from
// (e.g. ?from=2026-08-01T00:07:00Z) matches zero rows at every step even when the
// period is full of data, silently returning a confident-looking "computed over 0 of N
// slots, EUR 0.00" instead of visibly failing. Reject rather than fabricate (ADR-011
// rule 4).
var ErrLedgerRangeUnaligned = errors.New("from/to must be aligned to a tariff slot boundary")

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
func EarliestTariffSlot(ctx context.Context) (time.Time, error) {
	var ts sql.NullInt64
	if err := db.Instance.WithContext(ctx).Model(new(tariffValue)).
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
	LoadpointKWh           float64 // EV charging energy, all loadpoints summed; see modelledLoadKWh
	BatteryChargeKWh       float64
	BatteryDischargeKWh    float64
	BatterySocFrac         *float64 // 0..1, at slot start; nil if no battery configured
	PriceGrid, PriceFeedIn float64
}

// modelledLoadKWh is what W0/W1/W2 buy: the household's residual load plus whatever
// the loadpoints drew. HomeKWh alone is NOT total household consumption - it is a
// derived residual that core/site.go's updatePower already subtracts loadpoint charge
// power out of (homePower := gridPower + max(0,pvPower) + battery.Power -
// totalChargePower), precisely so that EV energy isn't double-counted against the
// "home" bucket elsewhere in evcc. The counterfactual worlds price what would have
// been bought for the *whole* site, cars included - modelling load from HomeKWh alone
// would price a household with no cars while W3 (the real grid meter) paid for every
// EV kWh. See ledger_worlds.go's doc comment for the worked-example consequence.
func (s slotData) modelledLoadKWh() float64 {
	return s.HomeKWh + s.LoadpointKWh
}

// Coverage reports what fraction of a period's slots the ledger could actually
// compute over. ADR-011 honesty rule 3: excluded slots (recovered, incomplete, or
// missing an input entirely) are dropped and reported, never scaled up to compensate.
// Coverage.Fraction is 0 both when the period had zero possible slots (TotalSlots==0,
// undefined) and when it had slots but none were valid (TotalSlots>0, ValidSlots==0,
// a genuine zero) - Fraction alone can't tell those apart. A caller that needs to
// isn't left guessing: check TotalSlots first, exactly like this struct's own
// coverage() constructor does before dividing.
type Coverage struct {
	ValidSlots int     `json:"validSlots"`
	TotalSlots int     `json:"totalSlots"`
	Fraction   float64 `json:"fraction"`
}

// ledgerSlotSet is the result of buildLedgerSlots: the valid per-slot data plus enough
// bookkeeping (whether PV/battery are configured at all, and the period's total slot
// count) to report coverage honestly.
type ledgerSlotSet struct {
	Slots        []slotData
	TotalSlots   int
	HasPV        bool
	HasBattery   bool
	HasLoadpoint bool
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
func queryGroupSlots(ctx context.Context, group string, from, to time.Time) (rows map[int64]groupSlotRow, hasEntities bool, err error) {
	var ids []int
	if err := db.Instance.WithContext(ctx).Model(new(entity)).Where(`"group" = ?`, group).Pluck("id", &ids).Error; err != nil {
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
	if err := db.Instance.WithContext(ctx).Table("meters").
		// recovered/incomplete were added by AutoMigrate with no DEFAULT, so every
		// pre-migration row has them NULL - COALESCE(...) = 0 treats NULL the same
		// as false (not recovered, not incomplete), matching
		// Collector.LastSlotEnergy's convention and batteryHistoryRows below.
		Select(`ts, COALESCE(SUM(energy), 0) AS energy, COALESCE(SUM(return_energy), 0) AS return_energy,
			AVG(soc_temp) AS soc_frac,
			MAX(CASE WHEN COALESCE(recovered, 0) = 0 AND COALESCE(incomplete, 0) = 0 THEN 0 ELSE 1 END) AS excluded`).
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
func queryTariffSlots(ctx context.Context, from, to time.Time) (map[int64]tariffSlot, error) {
	type row struct {
		Ts   int64
		Grid float64
		// gorm's default naming strategy maps field FeedIn to column "feed_in", but
		// tariffValue's own gorm tag (and the Select below) uses "feedin" - without
		// this explicit tag the scan silently left FeedIn at its zero value, so
		// every export in the ledger priced at EUR 0 regardless of what was on
		// record. Grid was unaffected only because its column name happens to equal
		// its lowercased field name. See TestQueryTariffSlotsBindsFeedIn.
		FeedIn float64 `gorm:"column:feedin"`
	}
	var res []row
	if err := db.Instance.WithContext(ctx).Model(new(tariffValue)).
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

// ErrLoadpointNoChargeMeter means a configured loadpoint has never written a single
// meters row for its whole recorded history - the signature of lp.chargeMeter == nil
// (core/loadpoint.go only wires lp.chargeEnergy, and so only ever calls its AddEnergy,
// when a charge meter is configured; a loadpoint WITH a meter still writes a
// zero-energy row every cycle even while nothing is plugged in, so "never any row" is
// not "not charging this period"). Modelling W0-W2's load from the home meter alone in
// this case would silently describe a car-free household while W3 (the real grid
// meter) still paid for every kWh that loadpoint drew - refuse instead of guessing
// (ADR-011 rule 4).
var ErrLoadpointNoChargeMeter = errors.New("a configured loadpoint has no charge-meter energy history; refusing to model a car-free household")

// loadpointEntityIDs returns the entity ids for every configured loadpoint.
func loadpointEntityIDs(ctx context.Context) ([]int, error) {
	var ids []int
	err := db.Instance.WithContext(ctx).Model(new(entity)).Where(`"group" = ?`, Loadpoint).Pluck("id", &ids).Error
	return ids, err
}

// verifyLoadpointChargeMeters refuses (ErrLoadpointNoChargeMeter) if any loadpoint in
// ids has never once written to the meters table, across all recorded history - not
// scoped to the requested period, since "has a charge meter" is a configuration fact,
// not something that becomes true or false slot by slot.
func verifyLoadpointChargeMeters(ctx context.Context, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	var withData []int
	if err := db.Instance.WithContext(ctx).Table("meters").Distinct("meter").Where("meter IN ?", ids).Pluck("meter", &withData).Error; err != nil {
		return err
	}
	if len(withData) < len(ids) {
		return ErrLoadpointNoChargeMeter
	}
	return nil
}

// buildLedgerSlots assembles the merged, filtered slot series every ledger
// computation runs on. A slot is included only if the grid meter, the home meter, the
// tariff (both prices), and - when the site has them configured - PV and the battery
// (including its SoC) all have a usable, non-excluded reading for that slot. Anything
// less and the slot is dropped, never interpolated (ADR-011 rules 3 and 5).
//
// includeLoadpoint and includeBattery each gate one computation's worth of extra
// requirements, so a caller that doesn't need a signal isn't filtered by it - one
// shared filter previously served every computation, so a week of BYD SoC read
// failures deleted a week from ComputeRealisedCost even though its own doc comment
// says it's independent of the battery entirely (ADR-011 Priority-4 finding).
//
//   - includeLoadpoint gates the loadpoint-charge-meter refusal
//     (ErrLoadpointNoChargeMeter) and LoadpointKWh's inclusion in the returned slots.
//   - includeBattery gates the battery query and the per-slot requirement that a
//     battery-configured site have a valid, non-excluded SoC reading before a slot
//     is included at all.
//
// ComputeRealisedCost passes false for both: it prices only the grid meter against
// tariffs (see its own doc comment). ComputeChain and ComputeLedger pass true for
// both: W0-W2, the routing/timing split and the decision replay all need
// slotData.modelledLoadKWh() and BatterySocFrac to be honest, not silently zero/nil.
//
// from must not precede the earliest priced tariff slot; see ErrBeforeTariffStart. The
// window is also capped at MaxLedgerRangeDays (ErrLedgerRangeTooLarge), and every query
// runs WithContext(ctx) so a client disconnect (or the range guard) stops work instead
// of running a query to completion nobody will read.
func buildLedgerSlots(ctx context.Context, from, to time.Time, includeLoadpoint, includeBattery bool) (*ledgerSlotSet, error) {
	if !to.After(from) {
		return nil, errors.New("invalid period: to must be after from")
	}
	if to.Sub(from) > time.Duration(MaxLedgerRangeDays)*24*time.Hour {
		return nil, ErrLedgerRangeTooLarge
	}
	if !from.Equal(from.Truncate(tariff.SlotDuration)) || !to.Equal(to.Truncate(tariff.SlotDuration)) {
		return nil, ErrLedgerRangeUnaligned
	}

	earliest, err := EarliestTariffSlot(ctx)
	if err != nil {
		return nil, err
	}
	if earliest.IsZero() || from.Before(earliest) {
		return nil, &ErrBeforeTariffStart{Earliest: earliest}
	}

	gridRows, hasGrid, err := queryGroupSlots(ctx, Grid, from, to)
	if err != nil {
		return nil, err
	}
	if !hasGrid {
		return nil, errors.New("no grid meter configured")
	}

	homeRows, hasHome, err := queryGroupSlots(ctx, Home, from, to)
	if err != nil {
		return nil, err
	}
	if !hasHome {
		return nil, errors.New("no home meter configured")
	}

	pvRows, hasPV, err := queryGroupSlots(ctx, PV, from, to)
	if err != nil {
		return nil, err
	}

	var batRows map[int64]groupSlotRow
	var hasBattery bool
	if includeBattery {
		batRows, hasBattery, err = queryGroupSlots(ctx, Battery, from, to)
		if err != nil {
			return nil, err
		}
	}

	var lpRows map[int64]groupSlotRow
	var hasLoadpoint bool
	if includeLoadpoint {
		lpIDs, err := loadpointEntityIDs(ctx)
		if err != nil {
			return nil, err
		}
		if err := verifyLoadpointChargeMeters(ctx, lpIDs); err != nil {
			return nil, err
		}
		lpRows, hasLoadpoint, err = queryGroupSlots(ctx, Loadpoint, from, to)
		if err != nil {
			return nil, err
		}
	}

	tariffRows, err := queryTariffSlots(ctx, from, to)
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

		if hasLoadpoint {
			l, ok := lpRows[u]
			if !ok || l.Excluded {
				continue
			}
			s.LoadpointKWh = l.Energy
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

	return &ledgerSlotSet{Slots: slots, TotalSlots: total, HasPV: hasPV, HasBattery: hasBattery, HasLoadpoint: hasLoadpoint}, nil
}
