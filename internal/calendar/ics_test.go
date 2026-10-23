package calendar

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// feed wraps events in a calendar file.
func feed(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + strings.Join(events, "") + "END:VCALENDAR\r\n"
}

func vevent(lines ...string) string {
	return "BEGIN:VEVENT\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

// list parses a file and expands it over [from, to), returning "start summary" lines in display time.
func list(t *testing.T, text string, display *time.Location, from, to string) ([]string, []string) {
	t.Helper()
	p, err := parse(text, display)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := time.ParseInLocation("2006-01-02", from, display)
	e, _ := time.ParseInLocation("2006-01-02", to, display)
	var out []string
	events := expand(p, "cal", f, e, display)
	sortEvents(events)
	for _, ev := range events {
		out = append(out, ev.Start.Format("2006-01-02 15:04")+" "+ev.Summary)
	}
	return out, p.notes
}

func sortEvents(es []Event) {
	slices.SortStableFunc(es, func(a, b Event) int { return a.Start.Compare(b.Start) })
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestParsesTheUsualFieldsFoldingAndEscapes(t *testing.T) {
	lagos := zone(t, "Africa/Lagos")
	text := feed(vevent(
		"UID:a1", "DTSTART:20261022T083000Z", "DTEND:20261022T090000Z",
		"SUMMARY:Stand\\, up\\; daily", "LOCATION:Room 4\\nFloor 2",
		"DESCRIPTION:A very long note that is folded onto a second line so that the reader must join the",
		" m back together",
		"BEGIN:VALARM", "ACTION:DISPLAY", "SUMMARY:ALARM MUST NOT BE THE TITLE", "END:VALARM",
	))
	p, err := parse(text, lagos)
	if err != nil || len(p.events) != 1 {
		t.Fatalf("%v %d", err, len(p.events))
	}
	e := p.events[0]
	if e.summary != "Stand, up; daily" || e.location != "Room 4\nFloor 2" || !strings.Contains(e.description, "join them back together") {
		t.Fatalf("%+v", e)
	}
	got, _ := list(t, text, lagos, "2026-10-22", "2026-10-23")
	eq(t, got, "2026-10-22 09:30 Stand, up; daily") // 08:30 UTC is 09:30 in Lagos
}

func TestTimeZonesAndFloatingTimes(t *testing.T) {
	ny := zone(t, "America/New_York")
	lagos := zone(t, "Africa/Lagos")
	text := feed(
		vevent("UID:1", "DTSTART;TZID=America/New_York:20261022T090000", "DTEND;TZID=America/New_York:20261022T100000", "SUMMARY:New York nine"),
		vevent("UID:2", "DTSTART:20261022T120000", "DTEND:20261022T130000", "SUMMARY:Floating noon"),
	)
	got, _ := list(t, text, lagos, "2026-10-22", "2026-10-23")
	// 09:00 EDT is 13:00 UTC is 14:00 in Lagos; a floating time is read in the display zone.
	eq(t, got, "2026-10-22 12:00 Floating noon", "2026-10-22 14:00 New York nine")
	got, _ = list(t, text, ny, "2026-10-22", "2026-10-23")
	eq(t, got, "2026-10-22 09:00 New York nine", "2026-10-22 12:00 Floating noon")

	// The calendar's own zone applies to floating times.
	withZone := strings.Replace(text, "VERSION:2.0", "VERSION:2.0\r\nX-WR-TIMEZONE:America/New_York", 1)
	got, _ = list(t, withZone, lagos, "2026-10-22", "2026-10-23")
	eq(t, got, "2026-10-22 14:00 New York nine", "2026-10-22 17:00 Floating noon")
}

func TestAnUnknownZoneNameIsReadAsUTCAndSaid(t *testing.T) {
	text := feed(vevent("UID:1", "DTSTART;TZID=Romance Standard Time:20261022T090000", "DTEND;TZID=Romance Standard Time:20261022T100000", "SUMMARY:Outlook meeting"))
	got, notes := list(t, text, time.UTC, "2026-10-22", "2026-10-23")
	eq(t, got, "2026-10-22 09:00 Outlook meeting")
	if len(notes) != 1 || !strings.Contains(notes[0], "Romance Standard Time") {
		t.Fatalf("%v", notes)
	}
}

func TestAllDayEventsStayOnTheirDaysInAnyZone(t *testing.T) {
	text := feed(
		vevent("UID:1", "DTSTART;VALUE=DATE:20261024", "DTEND;VALUE=DATE:20261025", "SUMMARY:Birthday"),
		vevent("UID:2", "DTSTART;VALUE=DATE:20261024", "DTEND;VALUE=DATE:20261027", "SUMMARY:Conference"),
		vevent("UID:3", "DTSTART;VALUE=DATE:20261030", "SUMMARY:No end date"),
	)
	for _, z := range []string{"Pacific/Auckland", "Africa/Lagos", "America/Los_Angeles"} {
		loc := zone(t, z)
		got, _ := list(t, text, loc, "2026-10-24", "2026-10-25")
		eq(t, got, "2026-10-24 00:00 Birthday", "2026-10-24 00:00 Conference")
		got, _ = list(t, text, loc, "2026-10-26", "2026-10-27")
		eq(t, got, "2026-10-24 00:00 Conference") // still on, on its third day
		got, _ = list(t, text, loc, "2026-10-27", "2026-10-30")
		eq(t, got) // ended: the end date is exclusive
		got, _ = list(t, text, loc, "2026-10-30", "2026-10-31")
		eq(t, got, "2026-10-30 00:00 No end date")
	}
}

func TestDurationsAndCancelledAndWindowEdges(t *testing.T) {
	text := feed(
		vevent("UID:1", "DTSTART:20261022T100000Z", "DURATION:PT1H30M", "SUMMARY:Duration"),
		vevent("UID:2", "DTSTART:20261022T100000Z", "DTEND:20261022T110000Z", "SUMMARY:Cancelled", "STATUS:CANCELLED"),
		vevent("UID:3", "DTSTART:20261021T230000Z", "DTEND:20261022T010000Z", "SUMMARY:Spans midnight"),
		vevent("UID:4", "DTSTART:20261022T230000Z", "DTEND:20261023T000000Z", "SUMMARY:Ends as the window does"),
		vevent("UID:5", "DTSTART:20261023T000000Z", "DTEND:20261023T010000Z", "SUMMARY:Starts as the window ends"),
	)
	got, _ := list(t, text, time.UTC, "2026-10-22", "2026-10-23")
	eq(t, got, "2026-10-21 23:00 Spans midnight", "2026-10-22 10:00 Duration", "2026-10-22 23:00 Ends as the window does")
	if d, ok := parseDuration("P1DT2H30M"); !ok || d != 26*time.Hour+30*time.Minute {
		t.Fatalf("%v %v", d, ok)
	}
	if d, ok := parseDuration("P1W"); !ok || d != 7*24*time.Hour {
		t.Fatalf("%v", d)
	}
	for _, bad := range []string{"", "1H", "PT", "PTxH", "P1X"} {
		if d, ok := parseDuration(bad); ok && d != 0 {
			t.Errorf("%q parsed as %v", bad, d)
		}
	}
}

func TestDailyAndWeeklyRepeats(t *testing.T) {
	text := feed(
		vevent("UID:d", "DTSTART:20261001T080000Z", "DTEND:20261001T083000Z", "RRULE:FREQ=DAILY;INTERVAL=3", "SUMMARY:Every third day"),
		vevent("UID:w", "DTSTART:20261005T090000Z", "DTEND:20261005T100000Z", "RRULE:FREQ=WEEKLY;BYDAY=MO,WE", "SUMMARY:Mon and Wed"),
	)
	got, _ := list(t, text, time.UTC, "2026-10-12", "2026-10-19")
	eq(t, got,
		"2026-10-12 09:00 Mon and Wed", "2026-10-13 08:00 Every third day",
		"2026-10-14 09:00 Mon and Wed", "2026-10-16 08:00 Every third day")
}

func TestCountEndDateAndExcludedDates(t *testing.T) {
	text := feed(
		vevent("UID:c", "DTSTART:20261001T090000Z", "DTEND:20261001T100000Z", "RRULE:FREQ=DAILY;COUNT=5", "SUMMARY:Five times"),
		vevent("UID:u", "DTSTART:20261001T110000Z", "DTEND:20261001T120000Z", "RRULE:FREQ=DAILY;UNTIL=20261003", "SUMMARY:Until the third"),
		vevent("UID:x", "DTSTART:20261001T130000Z", "DTEND:20261001T140000Z", "RRULE:FREQ=DAILY;COUNT=4", "EXDATE:20261002T130000Z", "SUMMARY:Skips the second"),
	)
	got, _ := list(t, text, time.UTC, "2026-10-01", "2026-10-10")
	eq(t, got,
		"2026-10-01 09:00 Five times", "2026-10-01 11:00 Until the third", "2026-10-01 13:00 Skips the second",
		"2026-10-02 09:00 Five times", "2026-10-02 11:00 Until the third",
		"2026-10-03 09:00 Five times", "2026-10-03 11:00 Until the third", "2026-10-03 13:00 Skips the second",
		"2026-10-04 09:00 Five times", "2026-10-04 13:00 Skips the second",
		"2026-10-05 09:00 Five times")
	// COUNT includes the occurrences before the window: looking only at the 4th and 5th finds what is left of it.
	got, _ = list(t, text, time.UTC, "2026-10-04", "2026-10-10")
	eq(t, got, "2026-10-04 09:00 Five times", "2026-10-04 13:00 Skips the second", "2026-10-05 09:00 Five times")
}

func TestMonthlyAndYearlyRepeats(t *testing.T) {
	text := feed(
		vevent("UID:1", "DTSTART:20260131T090000Z", "DTEND:20260131T100000Z", "RRULE:FREQ=MONTHLY;BYMONTHDAY=-1", "SUMMARY:Last day"),
		vevent("UID:2", "DTSTART:20260113T090000Z", "DTEND:20260113T100000Z", "RRULE:FREQ=MONTHLY;BYDAY=2TU", "SUMMARY:Second Tuesday"),
		vevent("UID:3", "DTSTART:20260130T150000Z", "DTEND:20260130T160000Z", "RRULE:FREQ=MONTHLY;BYDAY=-1FR", "SUMMARY:Last Friday"),
		vevent("UID:4", "DTSTART:20260131T120000Z", "DTEND:20260131T130000Z", "RRULE:FREQ=MONTHLY", "SUMMARY:The 31st"),
		vevent("UID:5", "DTSTART;VALUE=DATE:19900315", "RRULE:FREQ=YEARLY", "SUMMARY:Ada's birthday"),
		vevent("UID:6", "DTSTART;VALUE=DATE:20200229", "RRULE:FREQ=YEARLY", "SUMMARY:Leap day"),
	)
	got, _ := list(t, text, time.UTC, "2026-10-01", "2026-12-01")
	eq(t, got,
		"2026-10-13 09:00 Second Tuesday", "2026-10-30 15:00 Last Friday", "2026-10-31 09:00 Last day", "2026-10-31 12:00 The 31st",
		"2026-11-10 09:00 Second Tuesday", "2026-11-27 15:00 Last Friday", "2026-11-30 09:00 Last day")
	got, _ = list(t, text, time.UTC, "2026-03-01", "2026-03-31")
	if !contains(got, "2026-03-15 00:00 Ada's birthday") {
		t.Fatalf("%v", got)
	}
	got, _ = list(t, text, time.UTC, "2026-02-01", "2026-03-31")
	if contains(got, "Leap day") {
		t.Fatalf("29 February does not exist in 2026: %v", got)
	}
	got, _ = list(t, text, time.UTC, "2028-02-01", "2028-03-31")
	if !contains(got, "2028-02-29 00:00 Leap day") {
		t.Fatalf("%v", got)
	}
}

func contains(list []string, want string) bool {
	for _, l := range list {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// A weekly 09:00 in New York stays at 09:00 on the wall clock when the clocks change.
func TestARepeatKeepsItsWallClockTimeAcrossDaylightSavingChanges(t *testing.T) {
	ny := zone(t, "America/New_York")
	text := feed(vevent("UID:1", "DTSTART;TZID=America/New_York:20261026T090000", "DTEND;TZID=America/New_York:20261026T100000", "RRULE:FREQ=WEEKLY", "SUMMARY:Weekly nine"))
	got, _ := list(t, text, ny, "2026-10-26", "2026-11-17")
	eq(t, got, "2026-10-26 09:00 Weekly nine", "2026-11-02 09:00 Weekly nine", "2026-11-09 09:00 Weekly nine", "2026-11-16 09:00 Weekly nine") // the clocks went back on 1 November
	got, _ = list(t, text, time.UTC, "2026-10-26", "2026-11-04")
	eq(t, got, "2026-10-26 13:00 Weekly nine", "2026-11-02 14:00 Weekly nine") // an hour later in UTC after the change
}

func TestAChangedOccurrenceReplacesTheOneItNamesAndACancelledOneRemovesIt(t *testing.T) {
	text := feed(
		vevent("UID:s", "DTSTART:20261005T090000Z", "DTEND:20261005T100000Z", "RRULE:FREQ=WEEKLY", "SUMMARY:Standup"),
		vevent("UID:s", "RECURRENCE-ID:20261012T090000Z", "DTSTART:20261012T150000Z", "DTEND:20261012T160000Z", "SUMMARY:Standup (moved)"),
		vevent("UID:s", "RECURRENCE-ID:20261019T090000Z", "DTSTART:20261019T090000Z", "DTEND:20261019T100000Z", "SUMMARY:Standup", "STATUS:CANCELLED"),
	)
	got, _ := list(t, text, time.UTC, "2026-10-05", "2026-10-27")
	eq(t, got, "2026-10-05 09:00 Standup", "2026-10-12 15:00 Standup (moved)", "2026-10-26 09:00 Standup")
}

func TestARuleThisReaderCannotExpandShowsTheEventOnceAndSaysSo(t *testing.T) {
	text := feed(
		vevent("UID:1", "DTSTART:20261001T090000Z", "DTEND:20261001T100000Z", "RRULE:FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1", "SUMMARY:Last working day"),
		vevent("UID:2", "DTSTART:20261001T110000Z", "DTEND:20261001T120000Z", "RRULE:FREQ=HOURLY", "SUMMARY:Hourly"),
	)
	got, notes := list(t, text, time.UTC, "2026-10-01", "2026-12-01")
	eq(t, got, "2026-10-01 09:00 Last working day", "2026-10-01 11:00 Hourly")
	if len(notes) != 2 || !strings.Contains(notes[0], "BYSETPOS") || !strings.Contains(notes[1], "hourly") {
		t.Fatalf("%v", notes)
	}
}

func TestAnOldEndlessRepeatIsExpandedQuicklyAndANeverReachedOneIsNot(t *testing.T) {
	text := feed(
		vevent("UID:1", "DTSTART:19900101T090000Z", "DTEND:19900101T100000Z", "RRULE:FREQ=DAILY", "SUMMARY:Since 1990"),
		vevent("UID:2", "DTSTART:20300101T090000Z", "DTEND:20300101T100000Z", "RRULE:FREQ=DAILY", "SUMMARY:Starts in 2030"),
	)
	start := time.Now()
	got, _ := list(t, text, time.UTC, "2026-10-20", "2026-10-22")
	eq(t, got, "2026-10-20 09:00 Since 1990", "2026-10-21 09:00 Since 1990")
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

func TestNotACalendarAndDamagedFiles(t *testing.T) {
	for _, bad := range []string{"", "hello", "<html><body>Sign in</body></html>", "BEGIN:VEVENT\r\nEND:VEVENT"} {
		if _, err := parse(bad, time.UTC); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	// One broken event does not hide the rest, and the reader says what it skipped.
	text := feed(
		vevent("UID:1", "SUMMARY:No start"),
		vevent("UID:2", "DTSTART:garbage", "SUMMARY:Bad start"),
		vevent("UID:3", "DTSTART:20261022T090000Z", "DTEND:20261022T100000Z", "SUMMARY:Fine"),
	)
	got, notes := list(t, text, time.UTC, "2026-10-22", "2026-10-23")
	eq(t, got, "2026-10-22 09:00 Fine")
	if len(notes) != 2 {
		t.Fatalf("%v", notes)
	}
	// Whatever is in the file, parsing and expanding it does not panic.
	hostile := feed(
		vevent("UID:1", "DTSTART:20261022T090000Z", "RRULE:FREQ=WEEKLY;INTERVAL=0;BYDAY=XX;COUNT=-1"),
		vevent("DTSTART;TZID=:20261022T090000", "DURATION:P", "EXDATE:,,,", "RDATE:x", "RRULE:"),
		"BEGIN:VEVENT\r\nBEGIN:VALARM\r\nEND:VEVENT\r\n",
		"::::\r\n;;;\r\nDTSTART\r\n",
	)
	list(t, hostile, time.UTC, "2026-10-01", "2026-12-01")
}

func TestManyEventsParseQuickly(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString(vevent(fmt.Sprintf("UID:e%d", i), "DTSTART:20261022T090000Z", "DTEND:20261022T100000Z", fmt.Sprintf("SUMMARY:Event %d", i), "RRULE:FREQ=WEEKLY;COUNT=10"))
	}
	start := time.Now()
	got, _ := list(t, feed(b.String()), time.UTC, "2026-10-22", "2026-11-22")
	if len(got) != 5000*5 || time.Since(start) > 5*time.Second {
		t.Fatalf("%d events in %s", len(got), time.Since(start))
	}
}
