package v1

import (
	"encoding/json"
	"time"
)

func (i JobIdentity) Validate() error {
	if err := validIdentifier("job.identity.plugin_id", i.PluginID); err != nil {
		return err
	}
	if i.AccountID == 0 {
		return invalid("job.identity.account_id", "must be non-zero")
	}
	return validIdentifier("job.identity.id", i.ID)
}
func (t JobTimezone) Validate() error {
	if t.Name == "" {
		return invalid("job.timezone.name", "is required")
	}
	if _, err := time.LoadLocation(t.Name); err != nil {
		return invalid("job.timezone.name", "unknown timezone")
	}
	if t.DST != "" && t.DST != JobDSTReject && t.DST != JobDSTShiftForward && t.DST != JobDSTFirstFold && t.DST != JobDSTSecondFold {
		return invalid("job.timezone.dst", "unknown policy")
	}
	return nil
}
func (s JobSchedule) Validate() error {
	count := 0
	if s.OneShot != nil {
		count++
	}
	if s.Interval != nil {
		count++
	}
	if s.Calendar != nil {
		count++
	}
	if count != 1 {
		return invalid("job.schedule", "exactly one variant is required")
	}
	switch s.Kind {
	case JobScheduleOneShot:
		if s.OneShot == nil || s.OneShot.RunAtUnixMS <= 0 {
			return invalid("job.schedule.one_shot", "run_at_unix_ms must be positive")
		}
	case JobScheduleFixedDelay, JobScheduleFixedRate:
		if s.Interval == nil || s.Interval.IntervalMS < 1000 || s.Interval.IntervalMS > int64(MaxJobHorizonDays)*86400000 {
			return invalid("job.schedule.interval", "interval is outside bounds")
		}
	case JobScheduleCalendar:
		if s.Calendar == nil {
			return invalid("job.schedule.calendar", "calendar variant is required")
		}
		return validateCalendar(*s.Calendar)
	default:
		return invalid("job.schedule.kind", "unknown kind %q", s.Kind)
	}
	return nil
}
func (s JobSpec) Validate() error {
	if err := s.Identity.Validate(); err != nil {
		return err
	}
	if err := validIdentifier("job.handler", s.Handler); err != nil {
		return err
	}
	if err := validString("job.description", s.Description, false); err != nil {
		return err
	}
	if len(s.Description) > MaxJobDescriptionBytes {
		return invalid("job.description", "exceeds %d bytes", MaxJobDescriptionBytes)
	}
	if err := s.Schedule.Validate(); err != nil {
		return err
	}
	if err := s.Timezone.Validate(); err != nil {
		return err
	}
	if s.DeadlineMS < 1 || s.DeadlineMS > int64(MaxJobHorizonDays)*86400000 {
		return invalid("job.deadline_ms", "is outside bounds")
	}
	if s.MaxRuntimeMS < 1 || s.MaxRuntimeMS > 86400000 {
		return invalid("job.max_runtime_ms", "is outside bounds")
	}
	if s.Misfire != JobMisfireSkip && s.Misfire != JobMisfireFireOnce && s.Misfire != JobMisfireCatchUpOne {
		return invalid("job.misfire", "unknown policy")
	}
	if s.Overlap != JobOverlapSkipIfRunning && s.Overlap != JobOverlapCoalesceOnePending && s.Overlap != JobOverlapQueueOneBounded && s.Overlap != JobOverlapRejectQueueFull {
		return invalid("job.overlap", "unknown policy")
	}
	if len(s.Payload) > MaxJobPayloadBytes || (len(s.Payload) > 0 && !json.Valid(s.Payload)) {
		return invalid("job.payload", "must be bounded valid JSON")
	}
	return nil
}
func (r JobListRequest) Validate() error {
	if r.PluginID != "" {
		if err := validIdentifier("job.list.plugin_id", r.PluginID); err != nil {
			return err
		}
	}
	if r.Limit < 0 || r.Limit > MaxJobListItems {
		return invalid("job.list.limit", "must be between 0 and %d", MaxJobListItems)
	}
	return nil
}
func validateCalendar(c CalendarSchedule) error {
	limits := map[string][]int{"seconds": c.Seconds, "minutes": c.Minutes, "hours": c.Hours, "days_of_month": c.DaysOfMonth, "weekdays": c.Weekdays, "months": c.Months}
	for field, values := range limits {
		if len(values) > 32 {
			return invalid("job.schedule.calendar."+field, "has too many values")
		}
		for _, value := range values {
			max := 59
			if field == "hours" {
				max = 23
			}
			if field == "days_of_month" {
				max = 31
			}
			if field == "weekdays" {
				max = 6
			}
			if field == "months" {
				max = 12
			}
			if value < 0 || value > max {
				return invalid("job.schedule.calendar."+field, "contains an invalid value")
			}
		}
	}
	return nil
}
