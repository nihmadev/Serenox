package jobs

import (
	"fmt"
	"sort"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func nextRun(spec protocol.JobSpec, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(spec.Timezone.Name)
	if err != nil {
		return time.Time{}, err
	}
	base := after.In(location)
	switch spec.Schedule.Kind {
	case protocol.JobScheduleOneShot:
		return time.UnixMilli(spec.Schedule.OneShot.RunAtUnixMS), nil
	case protocol.JobScheduleFixedDelay, protocol.JobScheduleFixedRate:
		return after.Add(time.Duration(spec.Schedule.Interval.IntervalMS) * time.Millisecond), nil
	case protocol.JobScheduleCalendar:
		return nextCalendar(spec.Schedule.Calendar, base, location, spec.Timezone.DST)
	default:
		return time.Time{}, fmt.Errorf("unknown schedule kind %q", spec.Schedule.Kind)
	}
}

func nextCalendar(schedule *protocol.CalendarSchedule, after time.Time, location *time.Location, dst protocol.JobDSTPolicy) (time.Time, error) {
	seconds, minutes, hours := values(schedule.Seconds, 0, 59), values(schedule.Minutes, 0, 59), values(schedule.Hours, 0, 23)
	months, days, weekdays := values(schedule.Months, 1, 12), values(schedule.DaysOfMonth, 1, 31), values(schedule.Weekdays, 0, 6)
	start := after.Truncate(time.Second).Add(time.Second)
	for day := 0; day <= protocol.MaxJobHorizonDays; day++ {
		date := start.AddDate(0, 0, day)
		if !contains(months, int(date.Month())) || !contains(days, date.Day()) || !contains(weekdays, int(date.Weekday())) {
			continue
		}
		for _, hour := range hours {
			for _, minute := range minutes {
				for _, second := range seconds {
					candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, second, 0, location)
					if candidate.Before(start) {
						continue
					}
					candidate, valid := applyDSTPolicy(candidate, date, hour, minute, second, dst)
					if !valid || candidate.Before(start) {
						continue
					}
					return candidate, nil
				}
			}
		}
	}
	return time.Time{}, fmt.Errorf("calendar schedule exceeds horizon")
}

func applyDSTPolicy(candidate time.Time, date time.Time, hour, minute, second int, dst protocol.JobDSTPolicy) (time.Time, bool) {
	want := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, second, 0, time.UTC)
	got := time.Date(candidate.Year(), candidate.Month(), candidate.Day(), candidate.Hour(), candidate.Minute(), candidate.Second(), 0, time.UTC)
	if got != want {
		if dst != protocol.JobDSTShiftForward || got.After(want) {
			return candidate, false
		}
		return candidate.Add(want.Sub(got)), true
	}
	if dst == protocol.JobDSTSecondFold {
		alternate := candidate.Add(time.Hour)
		if sameWallTime(alternate, date, hour, minute, second) {
			_, currentOffset := candidate.Zone()
			_, alternateOffset := alternate.Zone()
			if currentOffset != alternateOffset {
				return alternate, true
			}
		}
	}
	return candidate, true
}

func sameWallTime(value, date time.Time, hour, minute, second int) bool {
	year, month, day := value.Date()
	return year == date.Year() && month == date.Month() && day == date.Day() && value.Hour() == hour && value.Minute() == minute && value.Second() == second
}
func values(value []int, min, max int) []int {
	if len(value) > 0 {
		result := append([]int(nil), value...)
		sort.Ints(result)
		return result
	}
	result := make([]int, 0, max-min+1)
	for i := min; i <= max; i++ {
		result = append(result, i)
	}
	return result
}
func contains(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
