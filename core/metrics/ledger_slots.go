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
	"math"
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

// ErrLedgerRangeInverted means to does not come after from - a reversed or identical
// period (e.g. ?from=2026-08-23&to=2026-08-22, or from==to). A malformed request, not
// a server fault, so it needs a sentinel here rather than a plain errors.New: the
// handler's mapping (savingsLedgerErrorStatus) only recognises specific errors and
// falls back to 500 for anything else - the exact failure mode ErrLoadpointNoChargeMeter
// had before it got its own case.
var ErrLedgerRangeInverted = errors.New("invalid period: to must be after from")

// ErrNoGridMeter and ErrNoHomeMeter mean the site has no meter of that group at all -
// a configuration problem the ledger refuses to work around, not a period with no
// data in it (that's handled by dropping slots, see buildLedgerSlots' doc comment).
var (
	ErrNoGridMeter = errors.New("no grid meter configured")
	ErrNoHomeMeter = errors.New("no home meter configured")
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

// EarliestTariffSlot returns the start of the first 15min slot for which a grid price
// is on record, or the zero time if none is. It deliberately does NOT require a
// feed-in price: this bound exists to refuse a window the site has no price record
// for at all, and a missing feed-in price is a per-slot condition buildLedgerSlots
// already handles slot by slot (either from the static fallback, see feedInFallback,
// or by dropping the slot). Requiring both here refused whole windows outright - on
// this site's own database it made the 3.7 days between the first recorded grid price
// and the first recorded feed-in price unqueryable, even though every one of those
// slots had a grid price and a grid meter reading.
func EarliestTariffSlot(ctx context.Context) (time.Time, error) {
	var ts sql.NullInt64
	if err := db.Instance.WithContext(ctx).Model(new(tariffValue)).
		Where("grid IS NOT NULL").
		Select("MIN(ts)").Scan(&ts).Error; err != nil {
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return time.Unix(ts.Int64, 0), nil
}

// EarliestChainSlot returns the earliest instant the world chain could produce a valid
// slot for - the latest of EarliestTariffSlot and the first recorded reading of every
// meter group buildLedgerSlots requires with includeLoadpoint/includeBattery set (grid,
// home, and whichever of PV/loadpoint/battery the site actually has configured; the
// battery additionally needs a non-NULL soc_temp).
//
// It exists because EarliestTariffSlot is NOT that bound, and a caller that treats it as
// one publishes a wrong number rather than a missing one. On this site's own database the
// tariffs table starts 2026-08-17 20:30 while the battery was commissioned on 2026-08-21,
// so a default 7-day window is accepted, computes the realised figure over 584 slots
// (EUR 34.69) and the chain over 254 (W3 = EUR 4.60), and every figure the card draws
// comes from the chain - telling a reader they paid a seventh of what they paid. The
// divergence is legitimate arithmetic; the fix is for the default window to land where
// the chain can actually be drawn, which needs this bound published.
//
// Returns the zero time - meaning "nowhere, don't narrow to it" - when the tariffs table
// has no priced slot, or when a configured group has no rows at all (the chain cannot
// compute anywhere, so there is no earlier-or-later instant that helps). It is a lower
// bound, not a guarantee: it says the chain has NO valid slot before this instant, not
// that the slot at it is valid.
func EarliestChainSlot(ctx context.Context) (time.Time, error) {
	earliest, err := EarliestTariffSlot(ctx)
	if err != nil || earliest.IsZero() {
		return time.Time{}, err
	}

	for _, req := range []struct {
		group      string
		requireSoc bool
	}{
		{Grid, false}, {Home, false}, {PV, false}, {Loadpoint, false}, {Battery, true},
	} {
		ts, configured, err := earliestGroupSlot(ctx, req.group, req.requireSoc)
		if err != nil {
			return time.Time{}, err
		}
		if !configured {
			continue
		}
		if ts.IsZero() {
			return time.Time{}, nil
		}
		if ts.After(earliest) {
			earliest = ts
		}
	}

	return earliest, nil
}

// earliestGroupSlot returns the first slot any entity in the group recorded. configured
// is false when the site has no entity in that group at all - distinct from a configured
// group with no rows, which returns a zero time with configured true, exactly the
// distinction queryGroupSlots' hasEntities exists to make.
func earliestGroupSlot(ctx context.Context, group string, requireSoc bool) (time.Time, bool, error) {
	var ids []int
	if err := db.Instance.WithContext(ctx).Model(new(entity)).Where(`"group" = ?`, group).Pluck("id", &ids).Error; err != nil {
		return time.Time{}, false, err
	}
	if len(ids) == 0 {
		return time.Time{}, false, nil
	}

	q := db.Instance.WithContext(ctx).Table("meters").
		Where("meter IN ? AND COALESCE(recovered, 0) = 0 AND COALESCE(incomplete, 0) = 0", ids)
	if requireSoc {
		q = q.Where("soc_temp IS NOT NULL")
	}

	var ts sql.NullInt64
	if err := q.Select("MIN(ts)").Scan(&ts).Error; err != nil {
		return time.Time{}, true, err
	}
	if !ts.Valid {
		return time.Time{}, true, nil
	}
	return time.Unix(ts.Int64, 0), true, nil
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

// MeterResidual is the A1 diagnostic: how far each valid slot's measured sources
// (grid import, PV, battery discharge) fall short of or exceed its measured sinks
// (grid export, home, loadpoint, battery charge). It is NOT an identity that must
// equal zero, even though HomeKWh is itself defined as this same residual at the
// power level (core/site.go's updatePower) - grid and home are both integrated from
// instantaneous power in the same accumulator pass, while PV, battery and loadpoint
// energy come from device-register deltas, each on their own polling cadence, booked
// into whichever 15-minute slot the read happened to land in. A read a few seconds
// either side of a slot boundary shows up here even though nothing is actually wrong.
// This residual is the noise floor under every euro figure in this payload - it does
// not itself carry a price (that's why it's kWh, not EUR), but a large one means the
// other figures shouldn't be trusted to a precision finer than this.
type MeterResidual struct {
	// SumKWh is Σ R over every valid slot in the period - the net drift, which can
	// partially cancel across slots.
	SumKWh float64 `json:"sumKWh"`
	// AbsSumKWh is Σ |R| - the total measurement noise, uncancelled.
	AbsSumKWh float64 `json:"absSumKWh"`
	Slots     int     `json:"slots"`
	// EurBand is AbsSumKWh priced at the period's mean grid rate: the same noise
	// floor, in the unit every other figure in this payload is denominated in, so a
	// caller can actually compare the two. Publishing the residual in kWh beside euro
	// contributions and calling it "the noise floor under every euro figure" left the
	// comparison to be done by eye and it never was: on this site's own database a
	// 1.345kWh residual sat under a -EUR 0.0664 Control figure the card rendered, in
	// the danger colour, as "the controller cost you money" - an assertion 7x smaller
	// than its own uncertainty. A band, not an error bar: the residual is a measured
	// discrepancy, not a distribution, and pricing it at the mean rate is the
	// cheapest honest way to put it on the same axis as the euros. A caller must treat
	// a contribution smaller than this as "inside the noise", never as a direction.
	EurBand float64 `json:"eurBand"`
}

// computeMeterResidual computes MeterResidual over an already-built, already-filtered
// slot set (see buildLedgerSlots) - every slot here already has a genuine, non-
// excluded reading for every meter the site has configured, so R = 0 - 0 for a group
// the site doesn't have (correctly, not fabricated) and a real per-slot figure for
// every group it does.
func computeMeterResidual(slots []slotData) MeterResidual {
	var sum, abssum, price float64
	for _, s := range slots {
		r := s.GridImportKWh - s.GridExportKWh + s.PVKWh + s.BatteryDischargeKWh - s.BatteryChargeKWh - s.HomeKWh - s.LoadpointKWh
		sum += r
		abssum += math.Abs(r)
		price += s.PriceGrid
	}

	res := MeterResidual{SumKWh: sum, AbsSumKWh: abssum, Slots: len(slots)}
	if n := len(slots); n > 0 {
		res.EurBand = abssum * price / float64(n)
	}
	return res
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
	// FeedInFallbackSlots counts the included slots whose feed-in price came from the
	// site's configured static tariff rather than from the tariffs table, and
	// FeedInFallbackPrice is that price. Reported in the payload's notes
	// (noteFeedInStaticFallback) so an imputed price is never indistinguishable from
	// an observed one.
	FeedInFallbackSlots int
	FeedInFallbackPrice float64
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

// tariffSlot is one 15min slot's realised grid/feed-in price. FeedIn is nil when no
// feed-in price was recorded for the slot - a real hole, never a zero price.
type tariffSlot struct {
	Grid   float64
	FeedIn *float64
}

// queryTariffSlots returns the slots in [from,to) that have a grid price on record.
// A slot without one is simply absent from the result - see buildLedgerSlots, which
// then excludes it from coverage rather than pricing it with a fabricated value. A
// slot WITH a grid price but without a feed-in price is returned with FeedIn nil;
// buildLedgerSlots decides whether the site's static feed-in tariff may stand in for
// it (feedInFallback) or whether the slot has to be dropped too.
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
		FeedIn *float64 `gorm:"column:feedin"`
	}
	var res []row
	if err := db.Instance.WithContext(ctx).Model(new(tariffValue)).
		Select("ts, grid, feedin").
		Where("ts >= ? AND ts < ? AND grid IS NOT NULL", from.Unix(), to.Unix()).
		Scan(&res).Error; err != nil {
		return nil, err
	}

	m := make(map[int64]tariffSlot, len(res))
	for _, r := range res {
		m[r.Ts] = tariffSlot{Grid: r.Grid, FeedIn: r.FeedIn}
	}
	return m, nil
}

// minFeedInWitnessSlots is how many recorded feed-in prices the tariffs table must
// hold before any of them count as corroboration. A single agreeing row proves only
// that the configured rate held at one instant, which says nothing about the slots
// the fallback is about to price; one full day of 15-minute slots is the smallest
// record that can be read as a history of the rate rather than a snapshot of it.
// Below that the fallback refuses and the affected slots stay excluded, which is the
// honest outcome for a site whose feed-in record is younger than the gap it has.
const minFeedInWitnessSlots = 96

// feedInFallback returns the price a slot with no recorded feed-in value may be
// priced at, or nil to keep excluding such slots.
//
// static is the site's currently configured feed-in price, and is non-nil only when
// that tariff declares itself api.TariffTypePriceStatic - a declaration that the
// price does not vary with time, which makes reading it a lookup rather than an
// interpolation across a gap (ADR-011 rule 3).
//
// The declaration alone is not enough, because the configs table keeps no history:
// applying today's configured value to a past slot asserts that it also held then,
// and that assertion breaks silently the moment the owner edits the rate (e.g. from
// the placeholder EUR 0.00 to the real EEG rate). So the fallback additionally
// requires the record to corroborate it: the tariffs table must hold at least
// minFeedInWitnessSlots recorded feed-in prices, and every one of them - MIN and MAX
// alike - must equal the configured value.
//
// The corroboration deliberately reads the WHOLE table, not the requested window.
// Window-scoped evidence is exactly as narrow as the caller makes it: a caller
// picking a window that starts after a rate change sees only post-change values,
// agrees with the current config, and imputes the new rate into slots billed at the
// old one - the failure this guard exists to prevent, reachable straight from the
// endpoint's query string. A rate change anywhere in the recorded history now makes
// MIN != MAX and the fallback refuses everywhere, which is the only reading the
// history-less configs table supports.
func feedInFallback(ctx context.Context, static *float64) (*float64, error) {
	if static == nil {
		return nil, nil
	}

	var res struct {
		N      int64
		Lo, Hi sql.NullFloat64
	}
	// COUNT/MIN/MAX all skip NULLs, so no WHERE is needed - and must not be added:
	// the point is to see every feed-in price the site ever recorded.
	if err := db.Instance.WithContext(ctx).Model(new(tariffValue)).
		Select("COUNT(feedin) AS n, MIN(feedin) AS lo, MAX(feedin) AS hi").
		Scan(&res).Error; err != nil {
		return nil, err
	}

	if res.N < minFeedInWitnessSlots || res.Lo.Float64 != *static || res.Hi.Float64 != *static {
		return nil, nil
	}
	return static, nil
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
// feedInStatic is the site's currently configured feed-in price, non-nil only when
// that tariff declares itself time-invariant. It lets a slot with a recorded grid
// price but no recorded feed-in price still be included - but only if feedInFallback's
// guard holds; see that function for why the declaration alone is not sufficient, and
// why the corroboration reads the whole tariffs table rather than this window.
//
// from must not precede the earliest priced tariff slot; see ErrBeforeTariffStart. The
// window is also capped at MaxLedgerRangeDays (ErrLedgerRangeTooLarge), and every query
// runs WithContext(ctx) so a client disconnect (or the range guard) stops work instead
// of running a query to completion nobody will read.
func buildLedgerSlots(ctx context.Context, from, to time.Time, includeLoadpoint, includeBattery bool, feedInStatic *float64) (*ledgerSlotSet, error) {
	if !to.After(from) {
		return nil, ErrLedgerRangeInverted
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
		return nil, ErrNoGridMeter
	}

	homeRows, hasHome, err := queryGroupSlots(ctx, Home, from, to)
	if err != nil {
		return nil, err
	}
	if !hasHome {
		return nil, ErrNoHomeMeter
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

	fallback, err := feedInFallback(ctx, feedInStatic)
	if err != nil {
		return nil, err
	}

	total := int(to.Sub(from) / tariff.SlotDuration)

	var fallbackSlots int
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
		feedIn := tv.FeedIn
		usedFallback := feedIn == nil
		if usedFallback {
			if fallback == nil {
				continue
			}
			feedIn = fallback
		}

		s := slotData{
			Start:         ts,
			GridImportKWh: g.Energy,
			GridExportKWh: g.ReturnEnergy,
			HomeKWh:       h.Energy,
			PriceGrid:     tv.Grid,
			PriceFeedIn:   *feedIn,
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
		// counted only here, after every other requirement has passed: a slot that
		// took the fallback price and was then dropped for an unrelated missing
		// reading is not a slot this substitution produced.
		if usedFallback {
			fallbackSlots++
		}
	}

	set := &ledgerSlotSet{Slots: slots, TotalSlots: total, HasPV: hasPV, HasBattery: hasBattery, HasLoadpoint: hasLoadpoint}
	if fallbackSlots > 0 {
		set.FeedInFallbackSlots = fallbackSlots
		set.FeedInFallbackPrice = *fallback
	}
	return set, nil
}
