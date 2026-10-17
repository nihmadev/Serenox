package jobs

import (
	"testing"
	"time"

	protocol "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

func TestCalendarDSTPolicies(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	date := time.Date(2024, time.March, 10, 0, 0, 0, 0, location)
	shifted, ok := applyDSTPolicy(time.Date(2024, time.March, 10, 2, 30, 0, 0, location), date, 2, 30, 0, protocol.JobDSTShiftForward)
	if !ok || shifted.Hour() != 3 || shifted.Minute() != 30 {
		t.Fatalf("shifted gap = %s, accepted = %v", shifted, ok)
	}
	foldDate := time.Date(2024, time.November, 3, 0, 0, 0, 0, location)
	first, ok := applyDSTPolicy(time.Date(2024, time.November, 3, 1, 30, 0, 0, location), foldDate, 1, 30, 0, protocol.JobDSTFirstFold)
	if !ok {
		t.Fatal("first fold was rejected")
	}
	second, ok := applyDSTPolicy(first, foldDate, 1, 30, 0, protocol.JobDSTSecondFold)
	if !ok || !second.After(first) {
		t.Fatalf("second fold = %s, first = %s, accepted = %v", second, first, ok)
	}
}
