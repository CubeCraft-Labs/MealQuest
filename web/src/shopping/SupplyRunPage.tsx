import { Archive, Check, CloudUpload, MessageSquare } from 'lucide-react'
import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { defaultWeek, isMonday, localNow, weekdayKicker, weekLabel } from '../plan/week.ts'
import { ApiError } from '../api/errors.ts'
import { formatClockTime, useHouseholdSettings } from '../settings/api.ts'
import { useBreakpoint, type Breakpoint } from '../shell/useBreakpoint.ts'
import { Button } from '../ui/Button.tsx'
import buttonStyles from '../ui/Button.module.css'
import { Checkbox } from '../ui/Checkbox.tsx'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import {
  useGenerateSupplyRun,
  useSendToChat,
  useSupplyRun,
  useToggles,
  WEEK_PARAM,
  type SupplyRunGroup,
  type SupplyRunItem,
  type Toggles,
} from './api.ts'
import { keep, savedSchedule, useSavedProblem } from './offline.ts'
import styles from './SupplyRunPage.module.css'

// SUPPLY_RUN is the Supply run screen.
export const SUPPLY_RUN = '/plan/shopping'

// SUPPLY_ARCHIVE is the Supply archive screen.
export const SUPPLY_ARCHIVE = '/plan/shopping/archive'

// sentAt is when the Supply run was sent to Google Chat as its status line
// says it, in the household's zone: "Sat 8:00 am".
export function sentAt(at: Date, timezone: string): string {
  const day = new Intl.DateTimeFormat('en-US', { weekday: 'short', timeZone: timezone }).format(at)
  return `${day} ${formatClockTime(localNow(timezone, at).time)}`
}

// ItemRow is one ingredient: on a current Supply run a checkbox, checked
// (grape) while it is to buy; unchecked, the name is struck through and
// "have it" stands in for the quantity. A read-only Supply run shows the
// same marks. An item toggled while the server cannot be reached
// says it will sync.
function ItemRow({ item, toggles }: { item: SupplyRunItem; toggles: Toggles | null }) {
  const name = (
    <>
      <span className={styles.name}>{item.name}</span>
      {toggles?.willSync.has(item.id) && (
        <span className={styles.willSync}>
          <Icon icon={CloudUpload} size="inline" />
          will sync
        </span>
      )}
    </>
  )
  return (
    <li className={[styles.item, !item.needed && styles.have].filter(Boolean).join(' ')}>
      {toggles === null ? (
        <span className={styles.readOnly}>
          <span className={styles.box} aria-hidden="true">
            {item.needed && <Icon icon={Check} size="inline" className={styles.mark} />}
          </span>
          {name}
        </span>
      ) : (
        <Checkbox
          className={styles.check}
          label={name}
          checked={item.needed}
          onChange={() => toggles.toggle({ id: item.id, needed: !item.needed })}
        />
      )}
      <span className={styles.qty}>{item.needed ? item.qty : 'have it'}</span>
    </li>
  )
}

// GroupCard is one dinner's items: its name in Bagel over its day.
function GroupCard({ group, toggles }: { group: SupplyRunGroup; toggles: Toggles | null }) {
  return (
    <section className={styles.group} aria-label={group.dinner.name}>
      <header className={styles.groupHeader}>
        <h2 className={styles.dinner}>{group.dinner.name}</h2>
        <span className={styles.day}>{weekdayKicker(group.date)}</span>
      </header>
      <ul className={styles.items}>
        {group.items.map((item) => (
          <ItemRow key={item.id} item={item} toggles={toggles} />
        ))}
      </ul>
    </section>
  )
}

// SentStatus is the status line once the Supply run was sent to Google Chat.
function SentStatus({ at, timezone }: { at: Date; timezone: string }) {
  return (
    <p className={styles.sent}>
      <Icon icon={MessageSquare} size="inline" />
      Sent to Google Chat · {sentAt(at, timezone)}
    </p>
  )
}

// UnconfirmedStatus is the status line while a send to Google Chat whose
// outcome is not known is recorded: it may or may not have been posted.
function UnconfirmedStatus({ at, timezone }: { at: Date; timezone: string }) {
  return (
    <p className={`${styles.sent} ${styles.unconfirmed}`}>
      <Icon icon={MessageSquare} size="inline" />
      Send to Google Chat not confirmed · {sentAt(at, timezone)} — check the space before sending again
    </p>
  )
}

// SendToChat posts the Supply run to Google Chat: "Send to Google Chat"
// until it is sent, or a send is unconfirmed, then "Send again", which
// posts it once more.
function SendToChat({ sent, pending, onSend }: { sent: boolean; pending: boolean; onSend: () => void }) {
  return (
    <Button className={styles.action} disabled={pending} onClick={onSend}>
      <Icon icon={MessageSquare} size="inline" />
      {pending ? 'Sending…' : sent ? 'Send again' : 'Send to Google Chat'}
    </Button>
  )
}

// ArchiveLink opens the Supply archive: an outline action from tablet up, a
// grape link on mobile.
function ArchiveLink({ layout }: { layout: Breakpoint }) {
  const className = layout === 'mobile' ? styles.archiveLink : [buttonStyles.button, buttonStyles.outline, styles.action].join(' ')
  return (
    <Link to={SUPPLY_ARCHIVE} className={className}>
      <Icon icon={Archive} size="inline" />
      {layout === 'mobile' ? 'Supply archive' : 'Archive'}
    </Link>
  )
}

// SupplyRunList is a week's Supply run (mocks 1l, 2b, 2f): the kicker with
// the week's dates, the title with the count still to buy of all items, and
// one card per dinner, stacked on mobile and in three masonry columns from
// tablet up, where Archive and Send to Google Chat are actions in the
// header; a send that fails says why, and one whose outcome is not known
// offers no retry and leaves the status line saying so. On a current Supply run each item toggles at once (useToggles); a
// toggle refused puts the item back and says why. An archived one, or any
// opened from the Supply archive (readOnly), is read-only and tagged
// Archived, and a toggle it refused is not offered again.
function SupplyRunList({ weekStart, timezone, layout, readOnly }: WeekProps) {
  const toggles = useToggles(weekStart)
  const send = useSendToChat(weekStart)
  const run = toggles.shown
  const sent = run.sent_to_chat_at !== null
  // A send whose outcome is not known is posted again only on request.
  const resend = sent || run.chat_send_unconfirmed_at !== null
  const unconfirmed = !send.isPending && run.chat_send_unconfirmed_at !== null && (
    <UnconfirmedStatus at={run.chat_send_unconfirmed_at} timezone={timezone} />
  )
  const sendUnconfirmed = send.error instanceof ApiError && send.error.code === 'chat_unconfirmed'
  const viewOnly = readOnly || run.status === 'archived'
  const wide = layout !== 'mobile'
  const failed = toggles.failed
  const failedItem = failed && run.groups.flatMap((group) => group.items).find((item) => item.id === failed.toggle.id)

  return (
    <div className={`${styles.page} ${styles[layout]}`}>
      <header className={styles.header}>
        <div className={styles.heading}>
          <p className={styles.kicker}>Supply run · {weekLabel(weekStart).dates}</p>
          <div className={styles.titleRow}>
            <h1 className={styles.title}>Shopping list</h1>
            {viewOnly && <Tag tone="grape">Archived</Tag>}
            <p className={styles.count}>
              {run.needed_count} / {run.total_count}
            </p>
          </div>
          <p className={styles.helper}>
            {run.status === 'archived'
              ? 'This list is in the Supply archive, as it was when the week ended.'
              : viewOnly
                ? "This week's list as it stands, read-only in the Supply archive."
                : "Uncheck anything you already have — it won't be counted."}
          </p>
        </div>
        {wide && (
          <div className={styles.actions}>
            <ArchiveLink layout={layout} />
            {!viewOnly && <SendToChat sent={resend} pending={send.isPending} onSend={() => send.mutate(resend)} />}
          </div>
        )}
      </header>
      {failed !== null && (
        <ErrorBanner onRetry={viewOnly ? undefined : () => toggles.toggle(failed.toggle)}>
          Couldn't mark {failedItem ? failedItem.name : 'an item'} {failed.toggle.needed ? 'to buy' : '“have it”'}.{' '}
          {failed.error.message}
        </ErrorBanner>
      )}
      {send.isError && !viewOnly && (
        <ErrorBanner onRetry={sendUnconfirmed ? undefined : () => send.mutate(resend)}>
          Couldn't send the shopping list to Google Chat. {send.error.message}
        </ErrorBanner>
      )}
      {wide && run.sent_to_chat_at !== null && <SentStatus at={run.sent_to_chat_at} timezone={timezone} />}
      {wide && unconfirmed}
      {run.groups.length === 0 ? (
        <EmptyState title="Nothing to buy">None of this week's dinners has a recipe with ingredients yet.</EmptyState>
      ) : (
        <div className={styles.groups} data-testid="supply-run-groups">
          {run.groups.map((group) => (
            <GroupCard key={`${group.date} ${group.dinner.id}`} group={group} toggles={viewOnly ? null : toggles} />
          ))}
        </div>
      )}
      {!wide && (
        <footer className={styles.footer}>
          {run.sent_to_chat_at !== null && <SentStatus at={run.sent_to_chat_at} timezone={timezone} />}
          {unconfirmed}
          <ArchiveLink layout={layout} />
        </footer>
      )}
    </div>
  )
}

// SkeletonGroups stands in for the dinner cards while the Supply run loads.
function SkeletonGroups({ layout }: { layout: Breakpoint }) {
  return (
    <section aria-busy="true" aria-label="Shopping list loading" className={`${styles.page} ${styles[layout]}`}>
      <Skeleton className={styles.skeletonTitle} />
      <div className={styles.groups}>
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} data-testid="skeleton-group" className={styles.skeletonGroup} />
        ))}
      </div>
    </section>
  )
}

type WeekProps = { weekStart: string; timezone: string; layout: Breakpoint; readOnly: boolean }

// WeekSupplyRun is the Supply run of the week starting on weekStart, or,
// when the week has none yet, the offer to make it from the week's plan —
// not offered read-only.
function WeekSupplyRun({ weekStart, timezone, layout, readOnly }: WeekProps) {
  const run = useSupplyRun(weekStart)
  const generate = useGenerateSupplyRun(weekStart)

  // Offline, the Supply run saved on this device stays shown; the offline
  // pill says it is saved data.
  if (run.isError && !(run.data !== undefined && run.error instanceof TypeError)) {
    return <ErrorBanner onRetry={() => void run.refetch()}>Couldn't load the shopping list. {run.error.message}</ErrorBanner>
  }
  if (run.isPending || generate.isPending) return <SkeletonGroups layout={layout} />
  if (run.data === null) {
    return (
      <div className={`${styles.page} ${styles[layout]}`}>
        {readOnly ? (
          <EmptyState title="No shopping list">This week has no Supply run in the Supply archive.</EmptyState>
        ) : (
          <EmptyState title="No shopping list yet" action={{ label: 'Make the shopping list', onClick: () => generate.mutate() }}>
            I'll gather the ingredients of this week's planned dinners into a Supply run.
          </EmptyState>
        )}
        {generate.isError && (
          <ErrorBanner onRetry={() => generate.mutate()}>Couldn't make the shopping list. {generate.error.message}</ErrorBanner>
        )}
      </div>
    )
  }
  return <SupplyRunList weekStart={weekStart} timezone={timezone} layout={layout} readOnly={readOnly} />
}

// SupplyRunPage is the Supply run (mock 1l on mobile, 2b on tablet, 2f on
// desktop) of the week the Plan opens on — next week once this week's
// plan_draft time has come, otherwise this week — or of the week chosen by
// ?week=. Opened from the Supply archive it is readOnly: tagged Archived,
// with no checkbox and nothing to make or send, whatever the week. Offline,
// where the settings cannot be read, it chooses the week by the schedule
// saved on this device.
export function SupplyRunPage({ readOnly = false }: { readOnly?: boolean }) {
  const problem = useSavedProblem()
  return (
    <>
      {problem !== null && (
        <ErrorBanner>Changes to the shopping list can't be kept on this device while offline. {problem.message}</ErrorBanner>
      )}
      <ChosenSupplyRun readOnly={readOnly} />
    </>
  )
}

function ChosenSupplyRun({ readOnly }: { readOnly: boolean }) {
  const layout = useBreakpoint()
  const settings = useHouseholdSettings()
  const [params] = useSearchParams()
  const chosen = params.get(WEEK_PARAM)
  // Read once, not on every render: a failed read reports a problem, which
  // renders this again.
  const [saved] = useState(() => keep(savedSchedule))

  if (chosen !== null && !isMonday(chosen)) {
    return <ErrorBanner>{`“${chosen}” is not the Monday of a week, as YYYY-MM-DD.`}</ErrorBanner>
  }
  // fetch rejects with a TypeError only when the network fails.
  const schedule = settings.data ?? (settings.error instanceof TypeError ? saved : null)
  if (settings.isError && !schedule) {
    return <ErrorBanner onRetry={() => void settings.refetch()}>Couldn't load the household settings. {settings.error.message}</ErrorBanner>
  }
  if (!schedule) return <SkeletonGroups layout={layout} />
  const week = chosen ?? defaultWeek(schedule, new Date())
  return <WeekSupplyRun key={week} weekStart={week} timezone={schedule.timezone} layout={layout} readOnly={readOnly} />
}
