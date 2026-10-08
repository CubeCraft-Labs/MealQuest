import { CalendarDays, Check, ChevronLeft, RefreshCw, type LucideIcon } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { Button } from '../ui/Button.tsx'
import buttonStyles from '../ui/Button.module.css'
import { Card } from '../ui/Card.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Select } from '../ui/Select.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import {
  GOOGLE_CONNECT_PATH,
  useChooseGoogleCalendar,
  useDisconnectGoogle,
  useGoogleCalendars,
  useGoogleConnection,
  type GoogleConnection,
} from './calendarApi.ts'
import styles from './CalendarSettingsPage.module.css'
import { Confirm } from './LeaveGuard.tsx'

// Settings · Google Calendar: connect the household's Google account, choose
// the calendar Push to Calendar writes the week's dinners to, and
// disconnect. A grant Google has refused shows a grape banner asking to
// reconnect, and nothing that needs Google is offered until then.
export function CalendarSettingsPage() {
  const connection = useGoogleConnection()
  return (
    <div className={styles.page}>
      <Header />
      <p className={styles.shared}>
        Push to Calendar adds one event per dinner. It is one-way: edits made in Google Calendar do not come back.
      </p>
      {connection.isPending && <Skeleton className={styles.loading} />}
      {connection.isError && (
        <ErrorBanner onRetry={() => void connection.refetch()}>
          Google Calendar status could not load: {connection.error.message}
        </ErrorBanner>
      )}
      {connection.isSuccess && <Connection connection={connection.data} />}
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
        <h1 className={styles.title}>Google Calendar</h1>
      </div>
    </header>
  )
}

function Connection({ connection }: { connection: GoogleConnection }) {
  switch (connection.status) {
    case 'not_connected':
      return (
        <Section title="Not connected">
          <p className={styles.text}>Connect the household's Google account to push each week's plan to one of its calendars.</p>
          <ConnectLink>Connect Google Calendar</ConnectLink>
        </Section>
      )
    case 'reconnect_required':
      return (
        <>
          <ReconnectBanner error={connection.last_sync_error} />
          <Section title="Calendar">
            <p className={styles.text}>
              {connection.calendar_name === null
                ? 'No calendar chosen. Choose one once Google Calendar is reconnected.'
                : `Dinners go to ${connection.calendar_name} once Google Calendar is reconnected.`}
            </p>
            <Disconnect calendar={connection.calendar_name} />
          </Section>
        </>
      )
    case 'connected':
      return (
        <Section title="Calendar">
          <p className={styles.connected}>
            <Icon icon={Check} size="inline" />
            {connection.calendar_name === null ? 'Connected' : `Connected · ${connection.calendar_name}`}
          </p>
          <CalendarPicker connection={connection} />
          <Disconnect calendar={connection.calendar_name} />
        </Section>
      )
  }
}

// ReconnectBanner is the grape banner of a grant Google refused, with why
// and Reconnect. Why and what to do are an alert, as ErrorBanner's message
// is: a calendar request that meets the refusal turns the screen to it.
function ReconnectBanner({ error }: { error: string | null }) {
  const titleId = useId()
  return (
    <section className={styles.reconnect} aria-labelledby={titleId}>
      <h2 id={titleId} className={styles.reconnectTitle}>
        Reconnect Google Calendar
      </h2>
      <div role="alert" className={styles.reconnectStatus}>
        {error !== null && <p className={styles.reconnectText}>{error}</p>}
        <p className={styles.reconnectText}>Nothing can be pushed to Calendar until you connect again.</p>
      </div>
      <ConnectLink icon={RefreshCw}>Reconnect</ConnectLink>
    </section>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const titleId = useId()
  return (
    <Card className={styles.card} role="region" aria-labelledby={titleId}>
      <h2 id={titleId} className={styles.cardTitle}>
        {title}
      </h2>
      {children}
    </Card>
  )
}

// ConnectLink opens the connect endpoint as a page: the server sends the
// browser on to Google's consent screen, and back here once Google answers.
function ConnectLink({ icon = CalendarDays, children }: { icon?: LucideIcon; children: ReactNode }) {
  return (
    <a href={GOOGLE_CONNECT_PATH} className={[buttonStyles.button, buttonStyles.primary, styles.connect].join(' ')}>
      <Icon icon={icon} size="inline" />
      {children}
    </a>
  )
}

// CalendarPicker chooses, from the calendars the account can write to, the
// one dinners are pushed to; a choice is saved at once. A calendar chosen
// before that has since been deleted or become read-only is no longer
// listed, and is said to be so.
function CalendarPicker({ connection }: { connection: GoogleConnection }) {
  const calendars = useGoogleCalendars(true)
  const choose = useChooseGoogleCalendar()

  if (calendars.isPending) return <Skeleton className={styles.pickerLoading} />
  if (calendars.isError) {
    return (
      <ErrorBanner onRetry={() => void calendars.refetch()}>
        Your calendars could not load: {calendars.error.message}
      </ErrorBanner>
    )
  }
  if (calendars.data.length === 0) {
    return <p className={styles.warning}>This Google account has no calendar MealQuest can add dinners to.</p>
  }

  const listed = calendars.data.some((calendar) => calendar.id === connection.calendar_id)
  const value = choose.isPending ? choose.variables : listed ? connection.calendar_id! : ''
  return (
    <>
      {connection.calendar_id === null && <p className={styles.text}>Choose the calendar to push dinners to.</p>}
      {connection.calendar_id !== null && !listed && (
        <p className={styles.warning}>
          {connection.calendar_name} was deleted or MealQuest can no longer add dinners to it. Choose another calendar.
        </p>
      )}
      <Select
        label="Calendar"
        value={value}
        disabled={choose.isPending}
        onChange={(event) => {
          choose.reset()
          choose.mutate(event.target.value)
        }}
      >
        {value === '' && (
          <option value="" disabled>
            Choose a calendar
          </option>
        )}
        {calendars.data.map((calendar) => (
          <option key={calendar.id} value={calendar.id}>
            {calendar.primary ? `${calendar.summary} (primary)` : calendar.summary}
          </option>
        ))}
      </Select>
      <p className={styles.status} role="status">
        {choose.isPending && 'Saving…'}
      </p>
      {choose.isError && (
        <p className={styles.error} role="alert">
          Not saved: {choose.error.message}
        </p>
      )}
      <p className={styles.hint}>
        After choosing another calendar, the next push writes the whole week there. Dinners already pushed stay where they
        are.
      </p>
    </>
  )
}

// Disconnect revokes MealQuest's access at Google, after asking.
function Disconnect({ calendar }: { calendar: string | null }) {
  const [confirming, setConfirming] = useState(false)
  const disconnect = useDisconnectGoogle()
  return (
    <>
      <div className={styles.actions}>
        <Button
          variant="outline"
          onClick={() => {
            disconnect.reset()
            setConfirming(true)
          }}
        >
          Disconnect
        </Button>
      </div>
      <Confirm
        open={confirming}
        title="Disconnect Google Calendar?"
        confirmLabel={disconnect.isPending ? 'Disconnecting…' : 'Disconnect'}
        cancelLabel="Keep connected"
        busy={disconnect.isPending}
        dismissible={!disconnect.isPending}
        error={disconnect.isError ? `Not disconnected: ${disconnect.error.message}` : null}
        onCancel={() => setConfirming(false)}
        onConfirm={() => disconnect.mutate(undefined, { onSuccess: () => setConfirming(false) })}
      >
        MealQuest gives up its access to your Google account and stops pushing dinners.{' '}
        {calendar === null ? 'Dinners already pushed' : `Dinners already pushed to ${calendar}`} stay on the calendar.
      </Confirm>
    </>
  )
}
