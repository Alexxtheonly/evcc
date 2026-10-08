package metrics

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/evcc-io/evcc/server/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type tariffValue struct {
	Timestamp   int64    `gorm:"column:ts;uniqueIndex"` // 15min boundary
	Grid        *float64 `gorm:"column:grid"`
	FeedIn      *float64 `gorm:"column:feedin"`
	Co2         *float64 `gorm:"column:co2"`
	Temperature *float64 `gorm:"column:temperature"`
}

func (tariffValue) TableName() string {
	return "tariffs"
}

func init() {
	db.Register(func(_ *gorm.DB) error {
		return db.Instance.AutoMigrate(new(tariffValue))
	})
}

// ErrInvalidUsage is returned for an unknown tariff usage
var ErrInvalidUsage = errors.New("invalid usage")

// tariffUsages are the deletable usages, named after their table column
var tariffUsages = []string{"grid", "feedin", "co2", "temperature"}

// DeleteTariffs removes the persisted values in [from,to). An empty usage drops
// the entire row, otherwise only that usage is cleared. Both bounds are
// required, a full wipe is /api/db/reset. The count is the number of affected
// rows; rows dropped by the cleanup are a subset of the cleared ones.
func DeleteTariffs(from, to time.Time, usage string) (int64, error) {
	if from.IsZero() || to.IsZero() {
		return 0, errors.New("missing from/to")
	}

	inRange := func() *gorm.DB {
		return db.Instance.Where("ts >= ? AND ts < ?", from.Unix(), to.Unix())
	}

	if usage == "" {
		res := inRange().Delete(new(tariffValue))
		return res.RowsAffected, res.Error
	}

	// guards the column interpolated below
	if !slices.Contains(tariffUsages, usage) {
		return 0, fmt.Errorf("%w: %s (valid: %s)", ErrInvalidUsage, usage, strings.Join(tariffUsages, ", "))
	}

	res := inRange().Model(new(tariffValue)).
		Where(usage+" IS NOT NULL").
		Update(usage, gorm.Expr("NULL"))
	if res.Error != nil {
		return 0, res.Error
	}

	// drop the rows that no longer hold any value
	err := inRange().
		Where("grid IS NULL AND feedin IS NULL AND co2 IS NULL AND temperature IS NULL").
		Delete(new(tariffValue)).Error

	return res.RowsAffected, err
}

// PersistTariffs stores the tariff values at the given 15min boundary, nil values omitted.
//
// A slot already on record is not skipped: each column is filled only where it is
// still NULL, and a value already recorded is never overwritten. Do not simplify this
// back to clause.OnConflict{DoNothing} - skipping the row makes a gap permanent: a
// usage whose tariff happened to be unavailable when the slot was first written stays
// NULL forever, even once the tariff comes back, and those slots drop out of the
// savings ledger's coverage (see core/metrics/ledger_slots.go).
func PersistTariffs(ts time.Time, grid, feedin, co2, temperature *float64) error {
	if grid == nil && feedin == nil && co2 == nil && temperature == nil {
		return nil
	}

	// COALESCE(<table>.col, excluded.col) is what makes this fill-only rather than
	// overwrite. `excluded` is the sqlite/postgres name for the row that failed to
	// insert; mysql spells it differently, so this upsert is dialect-specific - fine
	// while server/db/db.go only opens sqlite, and the reason the table name is read
	// off the model instead of being spelled out a second time here.
	table := tariffValue{}.TableName()
	fillNulls := make(map[string]any, len(tariffUsages))
	for _, u := range tariffUsages {
		fillNulls[u] = gorm.Expr(fmt.Sprintf("COALESCE(%s.%s, excluded.%s)", table, u, u))
	}

	return db.Instance.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "ts"}},
		DoUpdates: clause.Assignments(fillNulls),
	}).Create(&tariffValue{
		Timestamp:   ts.Unix(),
		Grid:        grid,
		FeedIn:      feedin,
		Co2:         co2,
		Temperature: temperature,
	}).Error
}

// TariffsBackfillTable holds grid prices recovered from elsewhere (contrib/tariff-backfill)
// for slots evcc never recorded. Only the band reference median reads it, and only where
// tariffs has no grid price: the savings ledger, Results and EarliestTariffSlot never see
// it. evcc does not create it; the backfill tool does.
const TariffsBackfillTable = "tariffs_backfill"

// GridPriceStats is the median grid import price (currency/kWh) of a window, how many
// slots it was taken from and on how many local calendar days they fall.
type GridPriceStats struct {
	Median  float64
	Samples int
	Days    int
}

// GridPriceMedian returns the median grid price of the slots in [from,to): recorded prices,
// plus backfilled ones for slots without a recorded price when the backfill table exists.
// Slots without a grid price are absent, never zero. No samples returns zero stats and no
// error: callers must check Samples.
func GridPriceMedian(from, to time.Time) (GridPriceStats, error) {
	var rows []struct {
		Ts   int64
		Grid float64
	}
	query := "SELECT ts, grid FROM tariffs WHERE ts >= ? AND ts < ? AND grid IS NOT NULL"
	args := []any{from.Unix(), to.Unix()}
	if db.Instance.Migrator().HasTable(TariffsBackfillTable) {
		query += " UNION ALL SELECT ts, grid FROM " + TariffsBackfillTable +
			" WHERE ts >= ? AND ts < ? AND grid IS NOT NULL" +
			" AND ts NOT IN (SELECT ts FROM tariffs WHERE grid IS NOT NULL)"
		args = append(args, from.Unix(), to.Unix())
	}
	if err := db.Instance.Raw(query, args...).Scan(&rows).Error; err != nil {
		return GridPriceStats{}, err
	}
	if len(rows) == 0 {
		return GridPriceStats{}, nil
	}
	prices := make([]float64, len(rows))
	days := make(map[string]bool)
	for i, r := range rows {
		prices[i] = r.Grid
		days[time.Unix(r.Ts, 0).Local().Format(time.DateOnly)] = true
	}
	slices.Sort(prices)
	res := GridPriceStats{Samples: len(prices), Days: len(days), Median: prices[len(prices)/2]}
	if mid := len(prices) / 2; len(prices)%2 == 0 {
		res.Median = (prices[mid-1] + prices[mid]) / 2
	}
	return res, nil
}
