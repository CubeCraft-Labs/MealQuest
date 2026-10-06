// The IANA time zones the browser knows, with their current UTC offsets, for
// the household time zone picker.

export type TimeZoneOption = {
  zone: string
  // "UTC-04:00"; "UTC+00:00" for UTC itself.
  offset: string
}

// utcOffset is zone's offset from UTC at instant, as "UTC+05:30".
export function utcOffset(zone: string, instant: Date): string {
  const name = new Intl.DateTimeFormat('en-US', { timeZone: zone, timeZoneName: 'longOffset' })
    .formatToParts(instant)
    .find((part) => part.type === 'timeZoneName')?.value
  if (name === undefined) throw new Error(`no UTC offset for time zone "${zone}"`)
  if (name === 'GMT') return 'UTC+00:00'
  if (!/^GMT[+-]\d\d:\d\d$/.test(name)) throw new Error(`time zone "${zone}" has an unreadable offset "${name}"`)
  return `UTC${name.slice(3)}`
}

// Zones the server accepts that Intl.supportedValuesOf('timeZone') leaves
// out: it lists only canonical zones, and UTC is not one of them.
const unlistedZones = ['UTC', 'Etc/UTC', 'GMT', 'Etc/GMT']

// timeZoneOptions lists every zone the browser supports, the unlisted UTC
// zones, and kept (the saved and chosen zones, which may be aliases the
// list leaves out), sorted by name.
export function timeZoneOptions(kept: string[], instant: Date): TimeZoneOption[] {
  const zones = new Set([...Intl.supportedValuesOf('timeZone'), ...unlistedZones, ...kept])
  return [...zones].sort().map((zone) => ({ zone, offset: utcOffset(zone, instant) }))
}

// zoneLabel reads a zone as words: "America/Argentina/Buenos_Aires" is
// "America / Argentina / Buenos Aires".
export function zoneLabel(zone: string): string {
  return zone.replaceAll('_', ' ').replaceAll('/', ' / ')
}

// matchesZone is whether every word of the search text is in option's name
// or offset, in any case.
export function matchesZone(option: TimeZoneOption, search: string): boolean {
  const needle = search.trim().toLowerCase()
  if (needle === '') return true
  const haystack = `${option.zone} ${zoneLabel(option.zone)} ${option.offset}`.toLowerCase()
  return needle.split(/\s+/).every((word) => haystack.includes(word))
}
