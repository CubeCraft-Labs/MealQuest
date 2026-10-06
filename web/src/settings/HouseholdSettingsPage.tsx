import { Check, ChevronLeft } from 'lucide-react'
import { useEffect, useId, useMemo, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { Button } from '../ui/Button.tsx'
import { Card } from '../ui/Card.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Select } from '../ui/Select.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { TextInput } from '../ui/TextInput.tsx'
import {
  householdSettingsProblems,
  useHouseholdSettings,
  useSaveHouseholdSettings,
  type HouseholdSettings,
  type HouseholdSettingsField,
  type HouseholdSettingsPatch,
} from './api.ts'
import styles from './HouseholdSettingsPage.module.css'
import { LeaveGuard } from './LeaveGuard.tsx'
import { matchesZone, timeZoneOptions, utcOffset, zoneLabel } from './timezones.ts'

// Days of the week as the server counts them, from Sunday = 0.
const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'] as const

// Settings · Household: the time zone and the times the household's
// reminders and weekly AI runs happen at. Every field is edited as a draft
// and saved together; only the changed fields are sent.
export function HouseholdSettingsPage() {
  const settings = useHouseholdSettings()
  const [dirty, setDirty] = useState(false)
  return (
    <div className={styles.page}>
      <Header />
      <p className={styles.shared}>Changes apply to everyone in the household.</p>
      {settings.isPending && <Skeleton className={styles.loading} />}
      {settings.isError && (
        <ErrorBanner onRetry={() => void settings.refetch()}>
          Household settings could not load: {settings.error.message}
        </ErrorBanner>
      )}
      {settings.isSuccess && <HouseholdSettingsForm saved={settings.data} onDirtyChange={setDirty} />}
      <LeaveGuard dirty={dirty} />
    </div>
  )
}

function Header() {
  const navigate = useNavigate()
  return (
    <header className={styles.header}>
      <IconButton label="Back to Settings" onClick={() => void navigate('/settings')}>
        <Icon icon={ChevronLeft} size="nav" />
      </IconButton>
      <div>
        <p className={styles.kicker}>Settings</p>
        <h1 className={styles.title}>Household</h1>
      </div>
    </header>
  )
}

// patchOf is the PATCH body for the fields draft changes.
function patchOf(saved: HouseholdSettings, draft: HouseholdSettings): HouseholdSettingsPatch {
  const patch: Record<string, string | number> = {}
  for (const field of Object.keys(saved) as HouseholdSettingsField[]) {
    if (draft[field] !== saved[field]) patch[field] = draft[field]
  }
  return patch as HouseholdSettingsPatch
}

type HouseholdSettingsFormProps = {
  saved: HouseholdSettings
  // Called with whether the form holds unsaved changes.
  onDirtyChange: (dirty: boolean) => void
}

function HouseholdSettingsForm({ saved, onDirtyChange }: HouseholdSettingsFormProps) {
  const [draft, setDraft] = useState(saved)
  const save = useSaveHouseholdSettings()
  const patch = patchOf(saved, draft)
  const dirty = Object.keys(patch).length > 0
  const problems = save.isError ? householdSettingsProblems(save.error) : { fields: {}, other: [] }

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])
  useEffect(() => () => onDirtyChange(false), [onDirtyChange])

  function edit(change: Partial<HouseholdSettings>) {
    save.reset()
    setDraft((current) => ({ ...current, ...change }))
  }

  function onSave() {
    save.mutate(patch, { onSuccess: setDraft })
  }

  return (
    <>
      <Section title="Time zone">
        <TimeZoneField
          value={draft.timezone}
          saved={saved.timezone}
          onChange={(timezone) => edit({ timezone })}
          error={problems.fields.timezone}
        />
        <p className={styles.hint}>Every time on this screen is in this zone.</p>
      </Section>

      <Section title="Dinner">
        <TimeField
          draft={draft}
          edit={edit}
          problems={problems.fields}
          name="dinner_time"
          label="Dinner time"
          hint="When dinner is on the table. Calendar events start then, and crock-pot dinners count back from it to show when to start."
        />
        <TimeField
          draft={draft}
          edit={edit}
          problems={problems.fields}
          name="reminder_time"
          label="Day-of reminder"
          hint="When we remind you what's for dinner tonight."
        />
        <TimeField
          draft={draft}
          edit={edit}
          problems={problems.fields}
          name="review_time"
          label="Rotation check"
          hint="When we ask whether to keep a new dinner: the day after you try a Quest-log dinner, we ask “Keep it in rotation?”"
        />
      </Section>

      <Section title="Every week">
        <DayTimeField
          draft={draft}
          edit={edit}
          problems={problems.fields}
          day="plan_draft_dow"
          time="plan_draft_time"
          label="Plan draft"
          hint="When the AI drafts next week's plan on the Draft board for you to adjust."
        />
        <DayTimeField
          draft={draft}
          edit={edit}
          problems={problems.fields}
          day="suggestions_dow"
          time="suggestions_time"
          label="New suggestions"
          hint="When a fresh batch of Side quests arrives to swipe through."
        />
        <DayTimeField
          draft={draft}
          edit={edit}
          problems={problems.fields}
          day="chat_autosend_dow"
          time="chat_autosend_time"
          label="Google Chat Supply run"
          hint="When the week's Supply run is sent to your Google Chat space."
        />
      </Section>

      <div className={styles.status} role="status">
        {save.isSuccess && !dirty && 'Changes saved.'}
      </div>
      {save.isError && (
        <div className={styles.errors} role="alert">
          {Object.keys(problems.fields).length > 0 && (
            <p className={styles.error}>Not saved: fix the settings marked below their fields.</p>
          )}
          {problems.other.map((reason) => (
            <p key={reason} className={styles.error}>
              Not saved: {reason}
            </p>
          ))}
        </div>
      )}

      <div className={styles.actions}>
        <Button className={styles.save} disabled={!dirty || save.isPending} onClick={onSave}>
          {save.isPending ? 'Saving…' : 'Save changes'}
        </Button>
      </div>
    </>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const titleId = useId()
  return (
    <Card role="region" aria-labelledby={titleId} className={styles.card}>
      <h2 id={titleId} className={styles.cardTitle}>
        {title}
      </h2>
      {children}
    </Card>
  )
}

function FieldError({ id, error }: { id: string; error: string | undefined }) {
  if (error === undefined) return null
  return (
    <p id={id} className={styles.error}>
      {error}
    </p>
  )
}

type FieldProps = {
  draft: HouseholdSettings
  edit: (change: Partial<HouseholdSettings>) => void
  problems: Partial<Record<HouseholdSettingsField, string>>
  label: string
  hint: string
}

type TimeFieldProps = FieldProps & { name: 'dinner_time' | 'reminder_time' | 'review_time' }

function TimeField({ draft, edit, problems, name, label, hint }: TimeFieldProps) {
  const hintId = useId()
  const errorId = useId()
  const error = problems[name]
  return (
    <div className={styles.field}>
      <TextInput
        label={label}
        type="time"
        className={styles.time}
        value={draft[name]}
        aria-invalid={error !== undefined}
        aria-describedby={error === undefined ? hintId : `${errorId} ${hintId}`}
        onChange={(event) => edit({ [name]: event.target.value })}
      />
      <FieldError id={errorId} error={error} />
      <p id={hintId} className={styles.hint}>
        {hint}
      </p>
    </div>
  )
}

type DayTimeFieldProps = FieldProps & {
  day: 'plan_draft_dow' | 'suggestions_dow' | 'chat_autosend_dow'
  time: 'plan_draft_time' | 'suggestions_time' | 'chat_autosend_time'
}

function DayTimeField({ draft, edit, problems, day, time, label, hint }: DayTimeFieldProps) {
  const hintId = useId()
  const dayErrorId = useId()
  const timeErrorId = useId()
  const dayError = problems[day]
  const timeError = problems[time]
  return (
    <div className={styles.field} role="group" aria-label={label}>
      <div className={styles.columns}>
        <Select
          label={`${label} day`}
          value={draft[day]}
          aria-invalid={dayError !== undefined}
          aria-describedby={dayError === undefined ? hintId : `${dayErrorId} ${hintId}`}
          onChange={(event) => edit({ [day]: Number(event.target.value) })}
        >
          {days.map((name, index) => (
            <option key={name} value={index}>
              {name}
            </option>
          ))}
        </Select>
        <TextInput
          label={`${label} time`}
          type="time"
          className={`${styles.time} ${styles.pairedTime}`}
          value={draft[time]}
          aria-invalid={timeError !== undefined}
          aria-describedby={timeError === undefined ? hintId : `${timeErrorId} ${hintId}`}
          onChange={(event) => edit({ [time]: event.target.value })}
        />
      </div>
      <FieldError id={dayErrorId} error={dayError} />
      <FieldError id={timeErrorId} error={timeError} />
      <p id={hintId} className={styles.hint}>
        {hint}
      </p>
    </div>
  )
}

type TimeZoneFieldProps = {
  value: string
  saved: string
  onChange: (zone: string) => void
  error: string | undefined
}

// TimeZoneField shows the chosen zone and its current UTC offset; Change
// opens a searchable list of every IANA zone.
function TimeZoneField({ value, saved, onChange, error }: TimeZoneFieldProps) {
  const [open, setOpen] = useState(false)
  const labelId = useId()
  const errorId = useId()
  const offset = useMemo(() => utcOffset(value, new Date()), [value])
  return (
    <div className={styles.field}>
      <span id={labelId} className={styles.groupLabel}>
        Time zone
      </span>
      <div
        className={styles.zoneRow}
        role="group"
        aria-labelledby={labelId}
        aria-describedby={error === undefined ? undefined : errorId}
      >
        <span className={styles.zoneValue}>
          <span className={styles.zoneName}>{zoneLabel(value)}</span>
          <span className={styles.offset}>{offset}</span>
        </span>
        <button type="button" className={styles.change} onClick={() => setOpen(true)}>
          Change
        </button>
      </div>
      <FieldError id={errorId} error={error} />
      {open && (
        <TimeZoneSheet
          value={value}
          saved={saved}
          onClose={() => setOpen(false)}
          onChoose={(zone) => {
            onChange(zone)
            setOpen(false)
          }}
        />
      )}
    </div>
  )
}

type TimeZoneSheetProps = {
  value: string
  // The saved zone, offered even after another is chosen.
  saved: string
  onClose: () => void
  onChoose: (zone: string) => void
}

function TimeZoneSheet({ value, saved, onClose, onChoose }: TimeZoneSheetProps) {
  const [search, setSearch] = useState('')
  const titleId = useId()
  const options = useMemo(() => timeZoneOptions([saved, value], new Date()), [saved, value])
  const shown = options.filter((option) => matchesZone(option, search))
  return (
    <Sheet open onClose={onClose} aria-labelledby={titleId}>
      <div className={styles.sheet}>
        <h2 id={titleId} className={styles.cardTitle}>
          Choose a time zone
        </h2>
        <TextInput
          label="Search time zones"
          type="search"
          value={search}
          placeholder="City, region or UTC offset"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => setSearch(event.target.value)}
        />
        <p className={styles.hint} role="status">
          {shown.length === 0 ? 'No time zone matches.' : `${shown.length} ${shown.length === 1 ? 'time zone' : 'time zones'}`}
        </p>
        <ul className={styles.zoneList} aria-label="Time zones">
          {shown.map((option) => {
            const current = option.zone === value
            return (
              <li key={option.zone}>
                <button
                  type="button"
                  className={styles.zoneOption}
                  aria-pressed={current}
                  aria-label={`${zoneLabel(option.zone)}, ${option.offset}`}
                  onClick={() => onChoose(option.zone)}
                >
                  <span className={styles.zoneName}>{zoneLabel(option.zone)}</span>
                  <span className={styles.offset}>{option.offset}</span>
                  {current && <Icon icon={Check} className={styles.check} />}
                </button>
              </li>
            )
          })}
        </ul>
      </div>
    </Sheet>
  )
}
