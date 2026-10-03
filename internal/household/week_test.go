package household

import (
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// TestMain runs the package with the process zone set to UTC, so any use of
// time.Local in place of the household zone shows up as a failure.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	m.Run()
}

func loadZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func date(t *testing.T, s string) store.LocalDate {
	t.Helper()
	d, err := store.ParseLocalDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func utc(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestTodayUsesHouseholdZone(t *testing.T) {
	chicago := loadZone(t, "America/Chicago")
	auckland := loadZone(t, "Pacific/Auckland")
	for _, tc := range []struct {
		now  string
		loc  *time.Location
		want string
	}{
		// 2026-10-05T04:30Z is Sunday 23:30 in Chicago (CDT, UTC−5) and
		// Monday 17:30 in Auckland (NZDT, UTC+13).
		{"2026-10-05T04:30:00Z", chicago, "2026-10-04"},
		{"2026-10-05T04:30:00Z", auckland, "2026-10-05"},
		// 2026-10-04T11:30Z is Sunday 06:30 in Chicago and already Monday
		// 00:30 in Auckland.
		{"2026-10-04T11:30:00Z", chicago, "2026-10-04"},
		{"2026-10-04T11:30:00Z", auckland, "2026-10-05"},
	} {
		if got := Today(utc(t, tc.now), tc.loc).String(); got != tc.want {
			t.Errorf("Today(%s, %s) = %s, want %s", tc.now, tc.loc, got, tc.want)
		}
	}
}

func TestSundayNightMondayMorningWeekAssignment(t *testing.T) {
	for _, tc := range []struct {
		zone, now     string
		wantDay       string
		wantWeekStart string
		wantPeriodKey string
		wantWeekday   time.Weekday
	}{
		// Sunday 23:59 local belongs to the week that started the previous
		// Monday; one minute later, Monday 00:00 starts a new week.
		{"America/Chicago", "2026-10-12T04:59:00Z", "2026-10-11", "2026-10-05", "2026-W41", time.Sunday},
		{"America/Chicago", "2026-10-12T05:00:00Z", "2026-10-12", "2026-10-12", "2026-W42", time.Monday},
		{"Pacific/Auckland", "2026-10-11T10:59:00Z", "2026-10-11", "2026-10-05", "2026-W41", time.Sunday},
		{"Pacific/Auckland", "2026-10-11T11:00:00Z", "2026-10-12", "2026-10-12", "2026-W42", time.Monday},
	} {
		loc := loadZone(t, tc.zone)
		now := utc(t, tc.now)
		if wd := now.In(loc).Weekday(); wd != tc.wantWeekday {
			t.Fatalf("%s in %s is %s, want %s", tc.now, tc.zone, wd, tc.wantWeekday)
		}
		today := Today(now, loc)
		if today.String() != tc.wantDay {
			t.Errorf("Today(%s, %s) = %s, want %s", tc.now, tc.zone, today, tc.wantDay)
		}
		if got := WeekStart(today).String(); got != tc.wantWeekStart {
			t.Errorf("WeekStart(%s) = %s, want %s", today, got, tc.wantWeekStart)
		}
		if got := PeriodKey(today); got != tc.wantPeriodKey {
			t.Errorf("PeriodKey(%s) = %s, want %s", today, got, tc.wantPeriodKey)
		}
	}
}

func TestWeekStartEveryWeekday(t *testing.T) {
	for _, d := range []string{
		"2026-10-05", "2026-10-06", "2026-10-07", "2026-10-08",
		"2026-10-09", "2026-10-10", "2026-10-11",
	} {
		if got := WeekStart(date(t, d)).String(); got != "2026-10-05" {
			t.Errorf("WeekStart(%s) = %s, want 2026-10-05", d, got)
		}
	}
	if got := WeekStart(date(t, "2026-10-12")).String(); got != "2026-10-12" {
		t.Errorf("WeekStart(2026-10-12) = %s, want 2026-10-12", got)
	}
}

func TestWeekDays(t *testing.T) {
	days, err := WeekDays(date(t, "2026-09-28"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"2026-09-28", "2026-09-29", "2026-09-30", "2026-10-01",
		"2026-10-02", "2026-10-03", "2026-10-04",
	}
	for i, d := range days {
		if d.String() != want[i] {
			t.Errorf("WeekDays[%d] = %s, want %s", i, d, want[i])
		}
	}
}

func TestWeekDaysAcrossDSTChange(t *testing.T) {
	// Chicago falls back on Sunday 2026-11-01 and Auckland springs forward on
	// Sunday 2026-09-27; neither shifts the dates of those weeks.
	for _, tc := range []struct{ start, last string }{
		{"2026-10-26", "2026-11-01"},
		{"2026-09-21", "2026-09-27"},
		{"2026-03-02", "2026-03-08"},
	} {
		days, err := WeekDays(date(t, tc.start))
		if err != nil {
			t.Fatal(err)
		}
		if days[0].String() != tc.start || days[6].String() != tc.last {
			t.Errorf("WeekDays(%s) = %s … %s, want %s … %s", tc.start, days[0], days[6], tc.start, tc.last)
		}
	}
}

func TestISOWeekYearBoundary(t *testing.T) {
	for _, tc := range []struct{ day, weekStart, key string }{
		{"2026-12-27", "2026-12-21", "2026-W52"},
		{"2026-12-28", "2026-12-28", "2026-W53"},
		{"2026-12-31", "2026-12-28", "2026-W53"},
		{"2027-01-01", "2026-12-28", "2026-W53"},
		{"2027-01-03", "2026-12-28", "2026-W53"},
		{"2027-01-04", "2027-01-04", "2027-W01"},
		{"2027-01-10", "2027-01-04", "2027-W01"},
		{"2026-01-01", "2025-12-29", "2026-W01"},
		{"2026-10-07", "2026-10-05", "2026-W41"},
	} {
		d := date(t, tc.day)
		if got := WeekStart(d).String(); got != tc.weekStart {
			t.Errorf("WeekStart(%s) = %s, want %s", tc.day, got, tc.weekStart)
		}
		if got := PeriodKey(d); got != tc.key {
			t.Errorf("PeriodKey(%s) = %s, want %s", tc.day, got, tc.key)
		}
	}
	days, err := WeekDays(date(t, "2026-12-28"))
	if err != nil {
		t.Fatal(err)
	}
	if days[6].String() != "2027-01-03" {
		t.Errorf("W53 Sunday = %s, want 2027-01-03", days[6])
	}
}

func TestWeekLabel(t *testing.T) {
	for _, tc := range []struct{ start, want string }{
		{"2026-10-05", "Week 41 · Oct 5 – 11"},
		{"2026-09-28", "Week 40 · Sep 28 – Oct 4"},
		{"2026-12-28", "Week 53 · Dec 28 – Jan 3"},
		{"2027-01-04", "Week 1 · Jan 4 – 10"},
	} {
		got, err := WeekLabel(date(t, tc.start))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("WeekLabel(%s) = %q, want %q", tc.start, got, tc.want)
		}
	}
}

func TestWeekStartRequired(t *testing.T) {
	for _, d := range []store.LocalDate{date(t, "2026-10-07"), date(t, "2026-10-11"), {}} {
		if _, err := WeekDays(d); err == nil {
			t.Errorf("WeekDays(%s) succeeded, want error", d)
		}
		if _, err := WeekLabel(d); err == nil {
			t.Errorf("WeekLabel(%s) succeeded, want error", d)
		}
	}
}

func TestZeroDatePanics(t *testing.T) {
	for name, fn := range map[string]func(){
		"WeekStart": func() { WeekStart(store.LocalDate{}) },
		"PeriodKey": func() { PeriodKey(store.LocalDate{}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s(zero) did not panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestLocalTime(t *testing.T) {
	for _, tc := range []struct {
		zone, day, hhmm string
		want            string // UTC instant
		wantWall        string // local wall clock of the result
	}{
		// Ordinary days.
		{"America/Chicago", "2026-10-05", "18:30", "2026-10-05T23:30:00Z", "2026-10-05 18:30 CDT"},
		{"America/Chicago", "2026-12-28", "00:00", "2026-12-28T06:00:00Z", "2026-12-28 00:00 CST"},
		{"Pacific/Auckland", "2026-10-05", "18:30", "2026-10-05T05:30:00Z", "2026-10-05 18:30 NZDT"},
		{"Pacific/Auckland", "2026-07-01", "23:59", "2026-07-01T11:59:00Z", "2026-07-01 23:59 NZST"},

		// Chicago spring forward, Sunday 2026-03-08: 02:00 CST → 03:00 CDT.
		{"America/Chicago", "2026-03-08", "01:59", "2026-03-08T07:59:00Z", "2026-03-08 01:59 CST"},
		{"America/Chicago", "2026-03-08", "02:00", "2026-03-08T08:00:00Z", "2026-03-08 03:00 CDT"},
		{"America/Chicago", "2026-03-08", "02:30", "2026-03-08T08:30:00Z", "2026-03-08 03:30 CDT"},
		{"America/Chicago", "2026-03-08", "03:00", "2026-03-08T08:00:00Z", "2026-03-08 03:00 CDT"},
		{"America/Chicago", "2026-03-08", "03:30", "2026-03-08T08:30:00Z", "2026-03-08 03:30 CDT"},

		// Chicago fall back, Sunday 2026-11-01: 02:00 CDT → 01:00 CST, so
		// 01:00–01:59 happens twice; the first (CDT) occurrence is chosen.
		{"America/Chicago", "2026-11-01", "00:59", "2026-11-01T05:59:00Z", "2026-11-01 00:59 CDT"},
		{"America/Chicago", "2026-11-01", "01:00", "2026-11-01T06:00:00Z", "2026-11-01 01:00 CDT"},
		{"America/Chicago", "2026-11-01", "01:30", "2026-11-01T06:30:00Z", "2026-11-01 01:30 CDT"},
		{"America/Chicago", "2026-11-01", "02:00", "2026-11-01T08:00:00Z", "2026-11-01 02:00 CST"},

		// Auckland spring forward, Sunday 2026-09-27: 02:00 NZST → 03:00 NZDT.
		{"Pacific/Auckland", "2026-09-27", "01:59", "2026-09-26T13:59:00Z", "2026-09-27 01:59 NZST"},
		{"Pacific/Auckland", "2026-09-27", "02:30", "2026-09-26T14:30:00Z", "2026-09-27 03:30 NZDT"},
		{"Pacific/Auckland", "2026-09-27", "03:00", "2026-09-26T14:00:00Z", "2026-09-27 03:00 NZDT"},

		// Auckland fall back, Sunday 2026-04-05: 03:00 NZDT → 02:00 NZST, so
		// 02:00–02:59 happens twice; the first (NZDT) occurrence is chosen.
		{"Pacific/Auckland", "2026-04-05", "01:59", "2026-04-04T12:59:00Z", "2026-04-05 01:59 NZDT"},
		{"Pacific/Auckland", "2026-04-05", "02:30", "2026-04-04T13:30:00Z", "2026-04-05 02:30 NZDT"},
		{"Pacific/Auckland", "2026-04-05", "03:00", "2026-04-04T15:00:00Z", "2026-04-05 03:00 NZST"},
	} {
		loc := loadZone(t, tc.zone)
		got, err := LocalTime(loc, date(t, tc.day), tc.hhmm)
		if err != nil {
			t.Fatalf("LocalTime(%s, %s, %s): %v", tc.zone, tc.day, tc.hhmm, err)
		}
		if !got.Equal(utc(t, tc.want)) {
			t.Errorf("LocalTime(%s, %s, %s) = %s, want %s", tc.zone, tc.day, tc.hhmm, got.UTC().Format(time.RFC3339), tc.want)
		}
		if wall := got.Format("2006-01-02 15:04 MST"); wall != tc.wantWall {
			t.Errorf("LocalTime(%s, %s, %s) wall = %s, want %s", tc.zone, tc.day, tc.hhmm, wall, tc.wantWall)
		}
		if got.Location() != loc {
			t.Errorf("LocalTime(%s, …) location = %s, want %s", tc.zone, got.Location(), loc)
		}
	}
}

func TestLocalTimeDeterministic(t *testing.T) {
	// The same ambiguous or skipped input always resolves to the same instant.
	chicago := loadZone(t, "America/Chicago")
	for _, tc := range []struct{ day, hhmm string }{{"2026-03-08", "02:30"}, {"2026-11-01", "01:30"}} {
		first, err := LocalTime(chicago, date(t, tc.day), tc.hhmm)
		if err != nil {
			t.Fatal(err)
		}
		for range 100 {
			again, err := LocalTime(chicago, date(t, tc.day), tc.hhmm)
			if err != nil {
				t.Fatal(err)
			}
			if !again.Equal(first) {
				t.Fatalf("LocalTime(%s, %s) = %s then %s", tc.day, tc.hhmm, first, again)
			}
		}
	}
}

func TestLocalTimeRejectsInvalid(t *testing.T) {
	chicago := loadZone(t, "America/Chicago")
	d := date(t, "2026-10-05")
	for _, hhmm := range []string{
		"", "9:30", "09:3", "24:00", "23:60", "09:30:00", "0930", " 09:30", "09:30 ", "9am", "-1:00",
	} {
		if got, err := LocalTime(chicago, d, hhmm); err == nil {
			t.Errorf("LocalTime(%q) = %s, want error", hhmm, got)
		} else if !strings.Contains(err.Error(), "HH:MM") {
			t.Errorf("LocalTime(%q) error %q does not name the HH:MM form", hhmm, err)
		}
	}
	if _, err := LocalTime(chicago, store.LocalDate{}, "09:30"); err == nil {
		t.Error("LocalTime(zero date) succeeded")
	}
}
