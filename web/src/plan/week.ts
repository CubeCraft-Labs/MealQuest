// Household-local calendar helpers for the weekly plan, mirroring
// internal/household/week.go. Weeks are ISO weeks, Monday to Sunday. Dates
// are household-local YYYY-MM-DD strings; arithmetic is done on UTC
// midnights, which have no DST, so a date plus seven days is always the same
// weekday a week later.

const localDate = /^(\d{4})-(\d{2})-(\d{2})$/

const DAY_MS = 24 * 60 * 60 * 1000

const WEEKDAYS = ['SUN', 'MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT'] as const

// midnightUTC reads date as midnight UTC, or throws when it is not a real
// YYYY-MM-DD date.
function midnightUTC(date: string): Date {
  const match = localDate.exec(date)
  if (match === null) throw new Error(`not a YYYY-MM-DD date: "${date}"`)
  const t = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])))
  if (formatDate(t) !== date) throw new Error(`not a real date: "${date}"`)
  return t
}

function formatDate(t: Date): string {
  return t.toISOString().slice(0, 10)
}

// addDays returns date moved by days, which may be negative.
export function addDays(date: string, days: number): string {
  return formatDate(new Date(midnightUTC(date).getTime() + days * DAY_MS))
}

// weekStart returns the Monday of the week containing date.
export function weekStart(date: string): string {
  const sinceMonday = (midnightUTC(date).getUTCDay() + 6) % 7
  return addDays(date, -sinceMonday)
}

// weekdayKicker is date's day as a tile kicker: "MON" to "SUN".
export function weekdayKicker(date: string): string {
  return WEEKDAYS[midnightUTC(date).getUTCDay()]!
}

// dayOfMonth is date's day of the month: 1 to 31.
export function dayOfMonth(date: string): number {
  return midnightUTC(date).getUTCDate()
}

const weekday = new Intl.DateTimeFormat('en-US', { weekday: 'long', timeZone: 'UTC' })

// weekdayName is date's day in full: "Monday" to "Sunday".
export function weekdayName(date: string): string {
  return weekday.format(midnightUTC(date))
}

// monday reads start as midnight UTC, or throws when it is not a Monday.
function monday(start: string): Date {
  const t = midnightUTC(start)
  if (t.getUTCDay() !== 1) throw new Error(`week start ${start} is not a Monday`)
  return t
}

// isoWeek is the ISO 8601 week number of the week starting on Monday start:
// the week holding that week's Thursday's year's first Thursday counts as 1.
function isoWeek(start: Date): number {
  const thursday = new Date(start.getTime() + 3 * DAY_MS)
  const yearStart = Date.UTC(thursday.getUTCFullYear(), 0, 1)
  return Math.floor((thursday.getTime() - yearStart) / DAY_MS / 7) + 1
}

const monthDay = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })

// WeekLabel is the week starting on a Monday as the plan's header shows it:
// "Week 41" over "Oct 5 – 11", or "Sep 28 – Oct 4" when the week spans two
// months. Together they are the API's week_label.
export type WeekLabel = { week: string; dates: string }

// weekLabel labels the week starting on Monday start.
export function weekLabel(start: string): WeekLabel {
  const first = monday(start)
  const last = new Date(first.getTime() + 6 * DAY_MS)
  const end = last.getUTCMonth() === first.getUTCMonth() ? String(last.getUTCDate()) : monthDay.format(last)
  return { week: `Week ${isoWeek(first)}`, dates: `${monthDay.format(first)} – ${end}` }
}

// localNow is the household-local date (YYYY-MM-DD) and clock (24-hour
// HH:MM) at instant now in IANA zone.
export function localNow(zone: string, now: Date): { date: string; time: string } {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  }).formatToParts(now)
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((p) => p.type === type)!.value
  return { date: `${part('year')}-${part('month')}-${part('day')}`, time: `${part('hour')}:${part('minute')}` }
}

// defaultWeek is the Monday of the week the plan opens on: next week once
// this week's plan_draft time (day of the week counting from Sunday = 0,
// 24-hour HH:MM, both household-local) has come, otherwise this week.
export function defaultWeek(schedule: { timezone: string; plan_draft_dow: number; plan_draft_time: string }, now: Date): string {
  const today = localNow(schedule.timezone, now)
  const thisWeek = weekStart(today.date)
  const draftDay = addDays(thisWeek, (schedule.plan_draft_dow + 6) % 7)
  const drafted = today.date > draftDay || (today.date === draftDay && today.time >= schedule.plan_draft_time)
  return drafted ? addDays(thisWeek, 7) : thisWeek
}
