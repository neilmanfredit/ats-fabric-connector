package main

import "time"

// projectedMonthEndCalls extrapolates month-end usage from calls recorded so
// far this month (build brief section 6.6.2). Elapsed time is floored at one
// hour so the projection isn't wildly unstable in the first minutes of a
// month.
func projectedMonthEndCalls(now time.Time, callsThisMonth int64) float64 {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonthStart := monthStart.AddDate(0, 1, 0)
	totalMonth := nextMonthStart.Sub(monthStart)

	elapsed := now.UTC().Sub(monthStart)
	if elapsed < time.Hour {
		elapsed = time.Hour
	}

	fraction := elapsed.Seconds() / totalMonth.Seconds()
	return float64(callsThisMonth) / fraction
}

// budgetExceeded reports whether projected month-end usage exceeds the
// configured fraction of the monthly call limit.
func budgetExceeded(projected float64, monthlyLimit int64, thresholdFraction float64) bool {
	return projected > float64(monthlyLimit)*thresholdFraction
}
