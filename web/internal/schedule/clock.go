package schedule

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // The Windows ZIP does not require a system IANA database.
)

const (
	dateLayout = "2006-01-02"
	wakeWindow = 5 * time.Minute
)

type wallClock struct {
	zone         *time.Location
	hour, minute int
}

func loadClock(config Config) (wallClock, error) {
	var clock wallClock
	hourAndMinute, err := time.Parse("15:04", config.Time)
	if len(config.Time) != 5 || err != nil {
		return clock, fmt.Errorf("%w: indique une heure au format HH:MM.", ErrInvalidConfig)
	}
	clock.hour, clock.minute = hourAndMinute.Hour(), hourAndMinute.Minute()
	zone := config.TimeZone
	if zone == "" && !config.Enabled {
		return clock, nil // The browser supplies its timezone when first configured.
	}
	if len(zone) > 128 || zone == "Local" || strings.HasPrefix(zone, "/") ||
		strings.Contains(zone, "..") || strings.ContainsAny(zone, "\\:") {
		return clock, fmt.Errorf("%w: choisis un fuseau horaire IANA valide.", ErrInvalidConfig)
	}
	clock.zone, err = time.LoadLocation(zone)
	if zone == "" || err != nil {
		return clock, fmt.Errorf("%w: choisis un fuseau horaire IANA valide.", ErrInvalidConfig)
	}
	return clock, nil
}

// civilDay uses UTC for calendar arithmetic. It is a date label, not an
// instant in the timezone where the brief is scheduled.
func civilDay(at time.Time, zone *time.Location) time.Time {
	y, m, d := at.In(zone).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// slot maps the selected wall time to an instant. A gap is skipped. In a fold
// the earlier occurrence wins, so waking during the second occurrence doesn't
// rerun a job missed an hour earlier.
func (clock wallClock) slot(day time.Time) time.Time {
	y, m, d := day.Date()
	label := time.Date(y, m, d, clock.hour, clock.minute, 0, 0, time.UTC)
	guess := time.Date(y, m, d, clock.hour, clock.minute, 0, 0, clock.zone)
	var first time.Time
	for _, sample := range []time.Time{guess.Add(-24 * time.Hour), guess, guess.Add(24 * time.Hour)} {
		_, offset := sample.Zone()
		at := label.Add(-time.Duration(offset) * time.Second)
		wall := at.In(clock.zone)
		if wall.Year() == y && wall.Month() == m && wall.Day() == d &&
			wall.Hour() == clock.hour && wall.Minute() == clock.minute && wall.Second() == 0 &&
			(first.IsZero() || at.Before(first)) {
			first = at
		}
	}
	return first
}

func (state State) availableSlot(clock wallClock, day time.Time) time.Time {
	if day.Format(dateLayout) <= state.claimedDate ||
		(state.Config.WeekdaysOnly && (day.Weekday() == time.Saturday || day.Weekday() == time.Sunday)) {
		return time.Time{}
	}
	at := clock.slot(day)
	if at.IsZero() || at.Before(state.armedAt) {
		return time.Time{}
	}
	return at
}

// Next returns the next eligible scheduled slot. During the wake window its
// original scheduled time can precede now by up to five minutes.
func (state State) Next(now time.Time) time.Time {
	if !state.Config.Enabled || now.IsZero() {
		return time.Time{}
	}
	clock, err := loadClock(state.Config)
	if err != nil {
		return time.Time{}
	}
	day := civilDay(now, clock.zone)
	if state.armedAt.After(now) {
		day = civilDay(state.armedAt, clock.zone)
	}
	if claimed, err := time.Parse(dateLayout, state.claimedDate); err == nil && !day.After(claimed) {
		day = claimed.AddDate(0, 0, 1)
	}
	for range 8 {
		at := state.availableSlot(clock, day)
		if !at.IsZero() && !now.After(at.Add(wakeWindow)) {
			return at
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}
}
