package calendar

import (
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// PropTime — момент из DTSTART/DTEND. Часовой пояс берётся из базы tzdata по TZID,
// а если там такого нет (Outlook пишет «Russian Standard Time») — из VTIMEZONE письма.
func PropTime(cal *ical.Calendar, p *ical.Prop) (t time.Time, allDay, floating bool, err error) {
	v := strings.TrimSpace(p.Value)
	if p.ValueType() == ical.ValueDate || len(v) == 8 {
		t, err = time.ParseInLocation("20060102", v, time.UTC)
		return t, true, false, err
	}
	if strings.HasSuffix(v, "Z") {
		t, err = time.ParseInLocation("20060102T150405Z", v, time.UTC)
		return t, false, false, err
	}
	wall, err := time.ParseInLocation("20060102T150405", v, time.UTC)
	if err != nil {
		return time.Time{}, false, false, err
	}
	tzid := strings.TrimPrefix(strings.Trim(p.Params.Get(ical.PropTimezoneID), `"`), "/")
	if tzid == "" {
		return wall, false, true, nil
	}
	if loc, err := time.LoadLocation(tzid); err == nil && tzid != "Local" {
		return inLocation(wall, loc), false, false, nil
	}
	for _, c := range cal.Children {
		if c.Name != ical.CompTimezone {
			continue
		}
		if id, _ := c.Props.Text(ical.PropTimezoneID); strings.TrimPrefix(id, "/") == tzid {
			if off, ok := vtimezoneOffset(c, wall); ok {
				return wall.Add(-time.Duration(off) * time.Second).In(time.FixedZone(tzid, off)), false, false, nil
			}
		}
	}
	return wall, false, true, nil // пояс неизвестен — показываем время как написано
}

func inLocation(wall time.Time, loc *time.Location) time.Time {
	return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc)
}

// vtimezoneOffset — смещение (в секундах) VTIMEZONE для местного времени wall:
// TZOFFSETTO последнего перехода STANDARD/DAYLIGHT, случившегося не позже wall.
// Время переходов сравнивается как местное (все — в UTC без сдвига).
func vtimezoneOffset(tz *ical.Component, wall time.Time) (int, bool) {
	var best time.Time
	bestOff, earliestFrom := 0, 0
	var earliest time.Time
	found, any := false, false
	for _, c := range tz.Children {
		if c.Name != ical.CompTimezoneStandard && c.Name != ical.CompTimezoneDaylight {
			continue
		}
		to, ok1 := parseUTCOffset(c.Props.Get(ical.PropTimezoneOffsetTo))
		from, ok2 := parseUTCOffset(c.Props.Get(ical.PropTimezoneOffsetFrom))
		ds := c.Props.Get(ical.PropDateTimeStart)
		if !ok1 || ds == nil {
			continue
		}
		start, err := time.ParseInLocation("20060102T150405", strings.TrimSuffix(ds.Value, "Z"), time.UTC)
		if err != nil {
			continue
		}
		if !any || start.Before(earliest) {
			earliest, earliestFrom, any = start, to, true
			if ok2 {
				earliestFrom = from
			}
		}
		onset := time.Time{}
		if opt, err := c.Props.RecurrenceRule(); err == nil && opt != nil {
			opt.Dtstart = start
			// Outlook пишет DTSTART в 1601 году, а rrule-go перебирает повторения
			// с ограничением и до наших лет не доходит. Годовое правило с BYMONTH
			// не зависит от года начала — переносим его поближе.
			if opt.Freq == rrule.YEARLY && opt.Count == 0 && start.Year() < wall.Year()-2 {
				opt.Dtstart = time.Date(wall.Year()-2, 1, 1, start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
				if len(opt.Bymonth) == 0 {
					opt.Bymonth = []int{int(start.Month())}
				}
				if len(opt.Bymonthday) == 0 && len(opt.Byweekday) == 0 && len(opt.Byyearday) == 0 {
					opt.Bymonthday = []int{start.Day()}
				}
			}
			if !opt.Until.IsZero() {
				opt.Until = opt.Until.Add(time.Duration(to) * time.Second) // UNTIL — в UTC
			}
			if r, err := rrule.NewRRule(*opt); err == nil {
				onset = r.Before(wall, true)
			}
		} else if !start.After(wall) {
			onset = start
		}
		for _, rd := range c.Props.Values(ical.PropRecurrenceDates) {
			for _, s := range strings.Split(rd.Value, ",") {
				if t, err := time.ParseInLocation("20060102T150405", s, time.UTC); err == nil && !t.After(wall) && t.After(onset) {
					onset = t
				}
			}
		}
		if !onset.IsZero() && (!found || onset.After(best)) {
			best, bestOff, found = onset, to, true
		}
	}
	if found {
		return bestOff, true
	}
	return earliestFrom, any
}

func parseUTCOffset(p *ical.Prop) (int, bool) {
	if p == nil {
		return 0, false
	}
	v := strings.TrimSpace(p.Value)
	if len(v) != 5 && len(v) != 7 || (v[0] != '+' && v[0] != '-') {
		return 0, false
	}
	h, err1 := strconv.Atoi(v[1:3])
	m, err2 := strconv.Atoi(v[3:5])
	s := 0
	var err3 error
	if len(v) == 7 {
		s, err3 = strconv.Atoi(v[5:7])
	}
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	off := h*3600 + m*60 + s
	if v[0] == '-' {
		off = -off
	}
	return off, true
}

// ensureTimezone добавляет VTIMEZONE для IANA-пояса tz, если его ещё нет:
// RFC 5545 требует его для каждого TZID, и не все серверы и клиенты
// знают пояса по имени. Правила переходов берутся из tzdata на год события.
func ensureTimezone(cal *ical.Calendar, tz string, at time.Time) {
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			if id, _ := c.Props.Text(ical.PropTimezoneID); id == tz {
				return
			}
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return
	}
	vt := ical.NewComponent(ical.CompTimezone)
	vt.Props.SetText(ical.PropTimezoneID, tz)
	year := at.In(loc).Year()
	jan := time.Date(year, 1, 1, 0, 0, 0, 0, loc)
	trans := transitions(jan, jan.AddDate(1, 0, 0))
	if len(trans) == 0 {
		_, off := jan.Zone()
		vt.Children = append(vt.Children, tzRule(ical.CompTimezoneStandard, time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), off, off, ""))
	} else {
		for _, t := range trans {
			_, before := t.Add(-time.Second).Zone()
			_, after := t.Zone()
			name := ical.CompTimezoneStandard
			if after > before {
				name = ical.CompTimezoneDaylight
			}
			wall := t.In(time.FixedZone("", before)) // местное время перехода по старому смещению
			wall = time.Date(1970, wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
			local := t.In(loc)
			week := (local.Day()-1)/7 + 1
			if local.AddDate(0, 0, 7).Month() != local.Month() {
				week = -1
			}
			rule := "FREQ=YEARLY;BYMONTH=" + itoa(int(local.Month())) + ";BYDAY=" + weekNum(week) + weekdays[local.Weekday()]
			// DTSTART правила — первый подходящий день 1970 года по этому правилу.
			wall = firstByRule(wall, local.Weekday(), week)
			vt.Children = append(vt.Children, tzRule(name, wall, before, after, rule))
		}
	}
	// VTIMEZONE — перед событиями, как пишут другие клиенты.
	cal.Children = append([]*ical.Component{vt}, cal.Children...)
}

var weekdays = [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

func weekNum(n int) string {
	if n < 0 {
		return "-1"
	}
	return itoa(n)
}

func firstByRule(t time.Time, wd time.Weekday, week int) time.Time {
	first := time.Date(t.Year(), t.Month(), 1, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	if week < 0 {
		last := first.AddDate(0, 1, -1)
		for last.Weekday() != wd {
			last = last.AddDate(0, 0, -1)
		}
		return last
	}
	for first.Weekday() != wd {
		first = first.AddDate(0, 0, 1)
	}
	return first.AddDate(0, 0, 7*(week-1))
}

// transitions — моменты смены смещения пояса в [from, to).
func transitions(from, to time.Time) []time.Time {
	var out []time.Time
	_, prev := from.Zone()
	for t := from; t.Before(to); t = t.Add(24 * time.Hour) {
		next := t.Add(24 * time.Hour)
		if _, off := next.Zone(); off != prev {
			lo, hi := t, next
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				if _, o := mid.Zone(); o == prev {
					lo = mid
				} else {
					hi = mid
				}
			}
			out = append(out, hi.Truncate(time.Second))
			prev = off
		}
	}
	return out
}

func tzRule(name string, start time.Time, from, to int, rule string) *ical.Component {
	c := ical.NewComponent(name)
	p := ical.NewProp(ical.PropDateTimeStart)
	p.Value = start.Format("20060102T150405")
	c.Props.Set(p)
	for name, v := range map[string]string{ical.PropTimezoneOffsetFrom: offset(from), ical.PropTimezoneOffsetTo: offset(to)} {
		op := ical.NewProp(name)
		op.Value = v
		c.Props.Set(op)
	}
	if rule != "" {
		r := ical.NewProp(ical.PropRecurrenceRule)
		r.Value = rule
		c.Props.Set(r)
	}
	return c
}

func offset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	h, m := sec/3600, sec%3600/60
	return sign + two(h) + two(m)
}

func two(n int) string { return string([]byte{byte('0' + n/10%10), byte('0' + n%10)}) }
