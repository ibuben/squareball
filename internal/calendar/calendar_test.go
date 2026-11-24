package calendar

import (
	"strings"
	"testing"
	"time"
)

func ics(body string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//t//EN\r\n" +
		strings.ReplaceAll(strings.TrimSpace(body), "\n", "\r\n") + "\r\nEND:VCALENDAR\r\n")
}

var (
	msk   = mustLoc("Europe/Moscow")
	paris = mustLoc("Europe/Paris")
)

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func TestExpandSingle(t *testing.T) {
	data := ics(`BEGIN:VEVENT
UID:a
DTSTAMP:20261101T000000Z
SUMMARY:Встреча
DTSTART;TZID=Europe/Moscow:20261201T120000
DTEND;TZID=Europe/Moscow:20261201T130000
END:VEVENT`)
	from := time.Date(2026, 12, 1, 0, 0, 0, 0, msk)
	got, err := Expand(data, from, from.AddDate(0, 0, 1), msk)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	if got[0].Summary != "Встреча" || !got[0].Start.Equal(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)) || got[0].Recurring {
		t.Fatalf("%+v", got[0])
	}
	if got, _ := Expand(data, from.AddDate(0, 0, 1), from.AddDate(0, 0, 2), msk); len(got) != 0 {
		t.Fatalf("outside range: %+v", got)
	}
}

func TestExpandWeeklyDSTWithExceptions(t *testing.T) {
	// Еженедельно по понедельникам в 10:00 по Парижу через переход на летнее время;
	// одно вхождение удалено, другое перенесено.
	data := ics(`BEGIN:VEVENT
UID:w
DTSTAMP:20260101T000000Z
SUMMARY:Планёрка
DTSTART;TZID=Europe/Paris:20260316T100000
DTEND;TZID=Europe/Paris:20260316T103000
RRULE:FREQ=WEEKLY;COUNT=5
EXDATE;TZID=Europe/Paris:20260323T100000
END:VEVENT
BEGIN:VEVENT
UID:w
DTSTAMP:20260101T000000Z
RECURRENCE-ID;TZID=Europe/Paris:20260406T100000
SUMMARY:Планёрка (перенос)
DTSTART;TZID=Europe/Paris:20260407T150000
DTEND;TZID=Europe/Paris:20260407T153000
END:VEVENT`)
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	got, err := Expand(data, from, from.AddDate(0, 2, 0), paris)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, o := range got {
		lines = append(lines, o.Start.In(paris).Format("01-02 15:04")+" "+o.Summary)
		if !o.Recurring || o.RecurrenceID == "" {
			t.Fatalf("recurring flags: %+v", o)
		}
	}
	want := "03-16 10:00 Планёрка|03-30 10:00 Планёрка|04-07 15:00 Планёрка (перенос)|04-13 10:00 Планёрка"
	if strings.Join(lines, "|") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(lines, "|"), want)
	}
	// 30 марта — уже летнее время: 10:00 по Парижу = 08:00 UTC.
	if got[1].Start.UTC().Hour() != 8 || got[0].Start.UTC().Hour() != 9 {
		t.Fatalf("DST: %v %v", got[0].Start.UTC(), got[1].Start.UTC())
	}
}

func TestExpandAllDayAndFloating(t *testing.T) {
	data := ics(`BEGIN:VEVENT
UID:d
DTSTAMP:20260101T000000Z
SUMMARY:Отпуск
DTSTART;VALUE=DATE:20261228
DTEND;VALUE=DATE:20261231
END:VEVENT
BEGIN:VEVENT
UID:f
DTSTAMP:20260101T000000Z
SUMMARY:Будильник
DTSTART:20261229T070000
RRULE:FREQ=DAILY;COUNT=3
END:VEVENT`)
	from := time.Date(2026, 12, 29, 0, 0, 0, 0, msk)
	got, err := Expand(data, from, from.AddDate(0, 0, 1), msk)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	if !got[0].AllDay || !got[0].Start.Equal(time.Date(2026, 12, 28, 0, 0, 0, 0, msk)) || !got[0].End.Equal(time.Date(2026, 12, 31, 0, 0, 0, 0, msk)) {
		t.Fatalf("all-day: %+v", got[0])
	}
	if got[1].Start.In(msk).Hour() != 7 || got[1].RecurrenceID == "" {
		t.Fatalf("floating: %+v", got[1])
	}
}

func TestBuildAndEdit(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	e := Event{Summary: "Созвон", Start: time.Date(2026, 7, 1, 12, 0, 0, 0, paris), End: time.Date(2026, 7, 1, 13, 0, 0, 0, paris), TZ: "Europe/Paris", RRule: "FREQ=WEEKLY;COUNT=3"}
	data, err := Build(nil, e, now)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"BEGIN:VTIMEZONE", "TZID:Europe/Paris", "BEGIN:DAYLIGHT", "TZOFFSETTO:+0200", "BYMONTH=3;BYDAY=-1SU",
		"DTSTART;TZID=Europe/Paris:20260701T120000", "RRULE:FREQ=WEEKLY;COUNT=3", "SUMMARY:Созвон"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	// Правка сохраняет неизвестные поля (напоминание) и меняет только форму.
	data = []byte(strings.Replace(s, "END:VEVENT", "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:x\r\nEND:VALARM\r\nEND:VEVENT", 1))
	f, err := FormEvent(data)
	if err != nil || f.TZ != "Europe/Paris" || f.Start.Hour() != 12 || f.RRule != "FREQ=WEEKLY;COUNT=3" || !f.Recurring {
		t.Fatalf("form: %v %+v", err, f)
	}
	f.Summary, f.RRule = "Созвон (новое время)", ""
	f.Start, f.End = f.Start.Add(time.Hour), f.End.Add(time.Hour)
	data, err = Build(data, f, now)
	if err != nil {
		t.Fatal(err)
	}
	s = string(data)
	if !strings.Contains(s, "TRIGGER:-PT15M") || strings.Contains(s, "RRULE:FREQ=WEEKLY") || !strings.Contains(s, "T130000") ||
		strings.Count(s, "BEGIN:VTIMEZONE") != 1 {
		t.Fatalf("edit:\n%s", s)
	}
	got, err := Expand(data, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil || len(got) != 1 || got[0].Start.UTC().Hour() != 11 {
		t.Fatalf("expand after edit: %v %+v", err, got)
	}
}

func TestBuildAllDayAndValidate(t *testing.T) {
	now := time.Now()
	data, err := Build(nil, Event{Summary: "ДР", Start: time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC), End: time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC), AllDay: true, TZ: "Europe/Moscow"}, now)
	if err != nil || !strings.Contains(string(data), "DTSTART;VALUE=DATE:20260509") || !strings.Contains(string(data), "DTEND;VALUE=DATE:20260510") ||
		strings.Contains(string(data), "VTIMEZONE") {
		t.Fatalf("%v\n%s", err, data)
	}
	for _, bad := range []Event{
		{Summary: "x"},
		{Summary: "x", Start: now, End: now.Add(-time.Hour)},
		{Summary: "x", Start: now, End: now, TZ: "Mars/Olympus"},
		{Summary: "x", Start: now, End: now, RRule: "FREQ=SOMETIMES"},
	} {
		if _, err := Build(nil, bad, now); err == nil {
			t.Fatalf("want error for %+v", bad)
		}
	}
}

func TestDeleteOccurrence(t *testing.T) {
	data := ics(`BEGIN:VEVENT
UID:w
DTSTAMP:20260101T000000Z
SUMMARY:S
DTSTART;TZID=Europe/Moscow:20260105T090000
DTEND;TZID=Europe/Moscow:20260105T100000
RRULE:FREQ=DAILY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:w
DTSTAMP:20260101T000000Z
RECURRENCE-ID;TZID=Europe/Moscow:20260106T090000
SUMMARY:S moved
DTSTART;TZID=Europe/Moscow:20260106T180000
DTEND;TZID=Europe/Moscow:20260106T190000
END:VEVENT`)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, msk)
	occ, _ := Expand(data, from, from.AddDate(0, 0, 10), msk)
	if len(occ) != 3 || occ[1].Summary != "S moved" {
		t.Fatalf("%+v", occ)
	}
	out, err := DeleteOccurrence(data, "w", occ[1].RecurrenceID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	occ, _ = Expand(out, from, from.AddDate(0, 0, 10), msk)
	if len(occ) != 2 || occ[0].Start.In(msk).Day() != 5 || occ[1].Start.In(msk).Day() != 7 {
		t.Fatalf("after delete: %+v", occ)
	}
}

func TestForStorageAndMerge(t *testing.T) {
	inv := ics(`METHOD:REQUEST
BEGIN:VEVENT
UID:m
DTSTAMP:20260101T000000Z
SEQUENCE:1
SUMMARY:Ревью
DTSTART:20260210T090000Z
DTEND:20260210T100000Z
ORGANIZER:mailto:boss@example.com
ATTENDEE;RSVP=TRUE;PARTSTAT=NEEDS-ACTION:mailto:Bob@example.com
END:VEVENT`)
	out, uid, err := ForStorage(inv, "bob@example.com", "ACCEPTED")
	if err != nil || uid != "m" || strings.Contains(string(out), "METHOD") || !strings.Contains(string(out), "PARTSTAT=ACCEPTED") ||
		strings.Contains(string(out), "RSVP") {
		t.Fatalf("%v %s\n%s", err, uid, out)
	}
	upd := []byte(strings.Replace(string(out), "SUMMARY:Ревью", "SUMMARY:Ревью v2", 1))
	merged, err := MergeInvite(out, upd)
	if err != nil || strings.Count(string(merged), "BEGIN:VEVENT") != 1 || !strings.Contains(string(merged), "Ревью v2") {
		t.Fatalf("%v\n%s", err, merged)
	}
}

func TestTimezoneWithoutDST(t *testing.T) {
	cal, _ := Decode(ics(`BEGIN:VEVENT
UID:x
DTSTAMP:20260101T000000Z
DTSTART:20260101T000000Z
END:VEVENT`))
	ensureTimezone(cal, "Asia/Tashkent", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	data, _ := Encode(cal)
	s := string(data)
	if !strings.Contains(s, "TZOFFSETTO:+0500") || strings.Contains(s, "DAYLIGHT") || strings.Contains(s, "RRULE") {
		t.Fatalf("%s", s)
	}
}
