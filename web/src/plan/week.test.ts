import { describe, expect, it } from 'vitest'
import {
  addDays,
  dayAndDate,
  dayOfMonth,
  defaultWeek,
  isMonday,
  localNow,
  untilLocalMidnight,
  weekdayKicker,
  weekdayName,
  weekLabel,
  weekStart,
} from './week.ts'

// SCHEDULE drafts the plan on Fridays (Sunday = 0) at 09:00 in New York.
const SCHEDULE = { timezone: 'America/New_York', plan_draft_dow: 5, plan_draft_time: '09:00' }

describe('week helpers', () => {
  it('finds the Monday of a week and moves by days across months and years', () => {
    expect(weekStart('2026-10-07')).toBe('2026-10-05')
    expect(weekStart('2026-10-05')).toBe('2026-10-05')
    expect(weekStart('2026-10-11')).toBe('2026-10-05')
    expect(addDays('2026-10-05', 7)).toBe('2026-10-12')
    expect(addDays('2026-10-05', -7)).toBe('2026-09-28')
    expect(addDays('2026-12-28', 7)).toBe('2027-01-04')
  })

  it('crosses a DST change without losing a day', () => {
    expect(addDays('2026-10-26', 7)).toBe('2026-11-02')
    expect(addDays('2027-03-08', 7)).toBe('2027-03-15')
  })

  it('labels weeks as the API does, by ISO week', () => {
    expect(weekLabel('2026-10-05')).toEqual({ week: 'Week 41', dates: 'Oct 5 – 11' })
    expect(weekLabel('2026-09-28')).toEqual({ week: 'Week 40', dates: 'Sep 28 – Oct 4' })
    expect(weekLabel('2026-12-28')).toEqual({ week: 'Week 53', dates: 'Dec 28 – Jan 3' })
    expect(weekLabel('2027-01-04')).toEqual({ week: 'Week 1', dates: 'Jan 4 – 10' })
  })

  it('names the day of a date as a tile kicker', () => {
    expect(weekdayKicker('2026-10-05')).toBe('MON')
    expect(weekdayKicker('2026-10-11')).toBe('SUN')
  })

  it('names the day of a date in full', () => {
    expect(weekdayName('2026-10-09')).toBe('Friday')
    expect(weekdayName('2026-10-11')).toBe('Sunday')
  })

  it('names the day of a date in full with its month and day', () => {
    expect(dayAndDate('2026-10-10')).toBe('Saturday, Oct 10')
    expect(dayAndDate('2026-11-01')).toBe('Sunday, Nov 1')
  })

  it('reads the day of the month of a date', () => {
    expect(dayOfMonth('2026-10-05')).toBe(5)
    expect(dayOfMonth('2026-10-31')).toBe(31)
    expect(() => dayOfMonth('2026-10-32')).toThrow('not a real date')
  })

  it('refuses a week start that is not a Monday, and a date that is not real', () => {
    expect(() => weekLabel('2026-10-06')).toThrow('week start 2026-10-06 is not a Monday')
    expect(() => addDays('2026-02-30', 1)).toThrow('not a real date')
    expect(() => weekStart('Oct 5')).toThrow('not a YYYY-MM-DD date')
  })

  it('reads the household-local date and clock in its zone', () => {
    // 03:30 UTC on Thursday is still Wednesday evening in New York.
    expect(localNow('America/New_York', new Date('2026-10-08T03:30:00Z'))).toEqual({ date: '2026-10-07', time: '23:30' })
    expect(localNow('Asia/Tokyo', new Date('2026-10-08T03:30:00Z'))).toEqual({ date: '2026-10-08', time: '12:30' })
  })

  it('opens on this week until the plan_draft time, then on next week', () => {
    expect(defaultWeek(SCHEDULE, new Date('2026-10-07T16:00:00Z'))).toBe('2026-10-05') // Wed noon
    expect(defaultWeek(SCHEDULE, new Date('2026-10-09T12:59:00Z'))).toBe('2026-10-05') // Fri 08:59
    expect(defaultWeek(SCHEDULE, new Date('2026-10-09T13:00:00Z'))).toBe('2026-10-12') // Fri 09:00
    expect(defaultWeek(SCHEDULE, new Date('2026-10-10T16:00:00Z'))).toBe('2026-10-12') // Sat
    expect(defaultWeek(SCHEDULE, new Date('2026-10-12T04:30:00Z'))).toBe('2026-10-12') // the next Mon, 00:30
  })

  it('counts a Sunday plan_draft day as the end of the Monday-start week', () => {
    const sunday = { ...SCHEDULE, plan_draft_dow: 0, plan_draft_time: '18:00' }
    expect(defaultWeek(sunday, new Date('2026-10-10T16:00:00Z'))).toBe('2026-10-05') // Sat
    expect(defaultWeek(sunday, new Date('2026-10-11T22:00:00Z'))).toBe('2026-10-12') // Sun 18:00
  })

  it('says whether a date is a real Monday', () => {
    expect(isMonday('2026-10-05')).toBe(true)
    expect(isMonday('2026-10-07')).toBe(false)
    expect(isMonday('2026-02-30')).toBe(false)
    expect(isMonday('next week')).toBe(false)
  })

  it('counts the time to the next midnight in the household zone, not UTC', () => {
    // 23:59:30.250 in New York is already 03:59 the next day in UTC.
    expect(untilLocalMidnight('America/New_York', new Date('2026-10-03T03:59:30.250Z'))).toBe(29_750)
    // At midnight itself the next is a day away.
    expect(untilLocalMidnight('America/New_York', new Date('2026-10-03T04:00:00Z'))).toBe(24 * 60 * 60 * 1000)
    // The same instant is 13:00 in Tokyo: eleven hours to go.
    expect(untilLocalMidnight('Asia/Tokyo', new Date('2026-10-03T04:00:00Z'))).toBe(11 * 60 * 60 * 1000)
  })

  it('counts a day shortened by the spring clock change as 23 hours', () => {
    const hour = 60 * 60 * 1000
    // 01:30 EST on Sunday March 8, 2026: clocks go forward at 02:00, so the
    // next midnight is 00:00 EDT on March 9 (04:00Z), 21.5 hours away.
    expect(untilLocalMidnight('America/New_York', new Date('2026-03-08T06:30:00Z'))).toBe(21.5 * hour)
    // 03:30 EDT, after the change: 20.5 hours to midnight.
    expect(untilLocalMidnight('America/New_York', new Date('2026-03-08T07:30:00Z'))).toBe(20.5 * hour)
    // At midnight starting the 23-hour day, the next midnight is 23 hours away.
    expect(untilLocalMidnight('America/New_York', new Date('2026-03-08T05:00:00Z'))).toBe(23 * hour)
    // Noon EST the day before: the change is after the coming midnight.
    expect(untilLocalMidnight('America/New_York', new Date('2026-03-07T17:00:00Z'))).toBe(12 * hour)
  })

  it('counts a day lengthened by the autumn clock change as 25 hours', () => {
    const hour = 60 * 60 * 1000
    // Midnight EDT on Sunday November 1, 2026: clocks go back at 02:00.
    expect(untilLocalMidnight('America/New_York', new Date('2026-11-01T04:00:00Z'))).toBe(25 * hour)
  })

  it('starts the next day at the clock change when the clocks skip midnight', () => {
    // Santiago goes from 24:00 -04 to 01:00 -03 on September 6, 2026 (04:00Z);
    // at 23:00 -04 on September 5 the next day starts an hour later.
    expect(untilLocalMidnight('America/Santiago', new Date('2026-09-06T03:00:00Z'))).toBe(60 * 60 * 1000)
  })
})
