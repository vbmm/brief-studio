package engine

import (
	"errors"
	"time"
)

const cmeScheduleURL = "https://www.cmegroup.com/trading-hours.html"

var ErrMarketOpen = errors.New("La fermeture du marché n’est pas confirmée. Rouvre Nouveau brief pour lancer une analyse.")

// MarketClosed reports whether the NQ and ES Globex matching session is
// scheduled to be closed. A false result does not guarantee the market is open.
func MarketClosed(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(newYork)
	day := local.Format("2006-01-02")
	hour := local.Hour()

	// CME's 2026 Thanksgiving equity-index halt: 12:00–17:00 CT.
	// https://www.cmegroup.com/trading-hours.html (Thanksgiving, CME Equity)
	if day == "2026-11-26" {
		return hour >= 13 && hour < 18
	}

	// Regular equity-index futures schedule: Sunday 17:00 CT to Friday
	// 16:00 CT, with daily 16:00–17:00 CT maintenance breaks.
	// https://www.cmegroup.com/markets/equities/sp-500-and-nasdaq-100-futures.html
	switch local.Weekday() {
	case time.Friday:
		return hour >= 17
	case time.Saturday:
		return true
	case time.Sunday:
		return hour < 18
	}
	// Cash-market holidays are not futures holidays. With no verified
	// special hours for a date, do not assume its regular maintenance break.
	return hour == 17 && !cashHolidays[day] && !earlyCashCloses[day]
}
