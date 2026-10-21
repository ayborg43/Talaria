package cron

import (
	"strings"
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	for _, tc := range []struct {
		spec, after, want string
	}{
		{"*/15 * * * *", "2026-10-20 10:07", "2026-10-20 10:15"},
		{"*/15 * * * *", "2026-10-20 10:45", "2026-10-20 11:00"},    // strictly after
		{"30 9 * * *", "2026-10-20 09:30", "2026-10-21 09:30"},      // not the same minute
		{"0 9 * * mon-fri", "2026-10-23 09:00", "2026-10-26 09:00"}, // Fri -> Mon
		{"0 9 * * 1-5", "2026-10-24 12:00", "2026-10-26 09:00"},     // Saturday
		{"0 0 1 jan *", "2026-10-20 00:00", "2027-01-01 00:00"},
		{"0 12 * * 7", "2026-10-20 00:00", "2026-10-25 12:00"}, // 7 is Sunday
		{"0 0 29 2 *", "2026-03-01 00:00", "2028-02-29 00:00"}, // next leap day
		{"5,35 8-9 * * *", "2026-10-20 08:40", "2026-10-20 09:05"},
		{"10-20/5 * * * *", "2026-10-20 10:00", "2026-10-20 10:10"},
		{"@hourly", "2026-10-20 10:30", "2026-10-20 11:00"},
		{"@daily", "2026-10-20 10:30", "2026-10-21 00:00"},
		{"@weekly", "2026-10-20 10:30", "2026-10-25 00:00"}, // Sunday
		{"@monthly", "2026-10-20 10:30", "2026-11-01 00:00"},
		{"@yearly", "2026-10-20 10:30", "2027-01-01 00:00"},
		{"@every 90m", "2026-10-20 10:30", "2026-10-20 12:00"},
		{"  0   9  *  *  *  ", "2026-10-20 10:00", "2026-10-21 09:00"}, // extra whitespace
	} {
		sch, err := ParseSchedule(tc.spec)
		if err != nil {
			t.Errorf("%q: %v", tc.spec, err)
			continue
		}
		got, ok := sch.Next(utc(tc.after))
		if !ok || !got.Equal(utc(tc.want)) {
			t.Errorf("%q after %s = %v (ok=%v), want %s", tc.spec, tc.after, got, ok, tc.want)
		}
	}
}

// With both day fields restricted, cron fires when either matches.
func TestDayOfMonthOrDayOfWeek(t *testing.T) {
	sch, _ := ParseSchedule("0 0 13 * fri")
	got, _ := sch.Next(utc("2026-10-01 00:00"))
	if !got.Equal(utc("2026-10-02 00:00")) { // a Friday that is not the 13th
		t.Fatalf("got %v", got)
	}
	got, _ = sch.Next(utc("2026-10-12 12:00"))
	if !got.Equal(utc("2026-10-13 00:00")) { // the 13th, a Tuesday
		t.Fatalf("got %v", got)
	}
}

func TestNextRespectsLocation(t *testing.T) {
	tokyo := time.FixedZone("JST", 9*3600)
	sch, _ := ParseSchedule("0 9 * * *")
	after := time.Date(2026, 10, 20, 12, 0, 0, 0, tokyo)
	got, _ := sch.Next(after)
	if want := time.Date(2026, 10, 21, 9, 0, 0, 0, tokyo); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestImpossibleAndOneShot(t *testing.T) {
	sch, _ := ParseSchedule("0 0 31 2 *")
	if _, ok := sch.Next(utc("2026-01-01 00:00")); ok {
		t.Fatal("Feb 31 should never fire")
	}

	once, err := ParseSchedule("@at 2026-10-21T09:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := once.Next(utc("2026-10-20 00:00")); !ok || !got.Equal(utc("2026-10-21 09:00")) {
		t.Fatalf("future one-shot: %v %v", got, ok)
	}
	if _, ok := once.Next(utc("2026-10-21 09:00")); ok {
		t.Fatal("one-shot must not fire again once its time has passed")
	}
}

func TestParseErrors(t *testing.T) {
	for _, spec := range []string{
		"", "* * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *",
		"* * * * 8", "*/0 * * * *", "5-1 * * * *", "foo * * * *", "* * * * funday",
		"@every 30s", "@every banana", "@every", "@at tomorrow", "@sometimes",
	} {
		if _, err := ParseSchedule(spec); err == nil {
			t.Errorf("%q should be rejected", spec)
		}
	}
	if _, err := ParseSchedule("@every 30s"); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Errorf("short interval should explain the minimum, got %v", err)
	}
}
