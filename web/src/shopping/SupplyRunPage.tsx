import { Archive, Check, MessageSquare } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { Unavailable } from '../plan/PlanPage.tsx'
import { defaultWeek, localNow, weekdayKicker, weekLabel, weekStart as weekOf } from '../plan/week.ts'
import { formatClockTime, useHouseholdSettings } from '../settings/api.ts'
import { useBreakpoint, type Breakpoint } from '../shell/useBreakpoint.ts'
import buttonStyles from '../ui/Button.module.css'
import { Checkbox } from '../ui/Checkbox.tsx'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import {
  useGenerateSupplyRun,
  useSupplyRun,
  useToggles,
  WEEK_PARAM,
  type SupplyRunGroup,
  type SupplyRunItem,
  type Toggles,
} from './api.ts'
import styles from './SupplyRunPage.module.css'

// SUPPLY_ARCHIVE is the Supply archive screen.
export const SUPPLY_ARCHIVE = '/plan/shopping/archive'

// CHAT_HINT is why Send to Google Chat is unavailable.
// TODO(Feature 19): send the Supply run to Google Chat once it is built.
const CHAT_HINT = 'Sending the shopping list to Google Chat arrives in a later update.'

// sentAt is when the Supply run was sent to Google Chat as its status line
// says it, in the household's zone: "Sat 8:00 am".
export function sentAt(at: Date, timezone: string): string {
  const day = new Intl.DateTimeFormat('en-US', { weekday: 'short', timeZone: timezone }).format(at)
  return `${day} ${formatClockTime(localNow(timezone, at).time)}`
}

// ItemRow is one ingredient: on a current Supply run a checkbox, checked
// (grape) while it is to buy; unchecked, the name is struck through and
// "have it" stands in for the quantity. An archived Supply run shows the
// same marks, read-only.
function ItemRow({ item, toggles }: { item: SupplyRunItem; toggles: Toggles | null }) {
  const name = <span className={styles.name}>{item.name}</span>
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
// header. On a current Supply run each item toggles at once (useToggles); a
// toggle refused puts the item back and says why. An archived one is
// read-only, and a toggle it refused is not offered again.
function SupplyRunList({ weekStart, timezone, layout }: { weekStart: string; timezone: string; layout: Breakpoint }) {
  const toggles = useToggles(weekStart)
  const run = toggles.shown
  const archived = run.status === 'archived'
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
            {archived && <Tag tone="grape">Archived</Tag>}
            <p className={styles.count}>
              {run.needed_count} / {run.total_count}
            </p>
          </div>
          <p className={styles.helper}>
            {archived
              ? 'This list is in the Supply archive, as it was when the week ended.'
              : "Uncheck anything you already have — it won't be counted."}
          </p>
        </div>
        {wide && (
          <div className={styles.actions}>
            <ArchiveLink layout={layout} />
            {!archived && (
              <Unavailable hint={CHAT_HINT} below className={styles.action}>
                <Icon icon={MessageSquare} size="inline" />
                Send to Google Chat
              </Unavailable>
            )}
          </div>
        )}
      </header>
      {failed !== null && (
        <ErrorBanner onRetry={archived ? undefined : () => toggles.toggle(failed.toggle)}>
          Couldn't mark {failedItem ? failedItem.name : 'an item'} {failed.toggle.needed ? 'to buy' : '“have it”'}.{' '}
          {failed.error.message}
        </ErrorBanner>
      )}
      {wide && run.sent_to_chat_at !== null && <SentStatus at={run.sent_to_chat_at} timezone={timezone} />}
      {run.groups.length === 0 ? (
        <EmptyState title="Nothing to buy">None of this week's dinners has a recipe with ingredients yet.</EmptyState>
      ) : (
        <div className={styles.groups} data-testid="supply-run-groups">
          {run.groups.map((group) => (
            <GroupCard key={`${group.date} ${group.dinner.id}`} group={group} toggles={archived ? null : toggles} />
          ))}
        </div>
      )}
      {!wide && (
        <footer className={styles.footer}>
          {run.sent_to_chat_at !== null && <SentStatus at={run.sent_to_chat_at} timezone={timezone} />}
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

// WeekSupplyRun is the Supply run of the week starting on weekStart, or,
// when the week has none yet, the offer to make it from the week's plan.
function WeekSupplyRun({ weekStart, timezone, layout }: { weekStart: string; timezone: string; layout: Breakpoint }) {
  const run = useSupplyRun(weekStart)
  const generate = useGenerateSupplyRun(weekStart)

  if (run.isError) {
    return <ErrorBanner onRetry={() => void run.refetch()}>Couldn't load the shopping list. {run.error.message}</ErrorBanner>
  }
  if (run.isPending || generate.isPending) return <SkeletonGroups layout={layout} />
  if (run.data === null) {
    return (
      <div className={`${styles.page} ${styles[layout]}`}>
        <EmptyState title="No shopping list yet" action={{ label: 'Make the shopping list', onClick: () => generate.mutate() }}>
          I'll gather the ingredients of this week's planned dinners into a Supply run.
        </EmptyState>
        {generate.isError && (
          <ErrorBanner onRetry={() => generate.mutate()}>Couldn't make the shopping list. {generate.error.message}</ErrorBanner>
        )}
      </div>
    )
  }
  return <SupplyRunList weekStart={weekStart} timezone={timezone} layout={layout} />
}

// SupplyRunPage is the Supply run (mock 1l on mobile, 2b on tablet, 2f on
// desktop) of the week the Plan opens on — next week once this week's
// plan_draft time has come, otherwise this week — or of the week chosen by
// ?week=, such as a week in the Supply archive.
export function SupplyRunPage() {
  const layout = useBreakpoint()
  const settings = useHouseholdSettings()
  const [params] = useSearchParams()
  const chosen = params.get(WEEK_PARAM)

  if (chosen !== null && !isMonday(chosen)) {
    return <ErrorBanner>{`“${chosen}” is not the Monday of a week, as YYYY-MM-DD.`}</ErrorBanner>
  }
  if (settings.isError) {
    return <ErrorBanner onRetry={() => void settings.refetch()}>Couldn't load the household settings. {settings.error.message}</ErrorBanner>
  }
  if (!settings.data) return <SkeletonGroups layout={layout} />
  const week = chosen ?? defaultWeek(settings.data, new Date())
  return <WeekSupplyRun key={week} weekStart={week} timezone={settings.data.timezone} layout={layout} />
}

// isMonday says whether date is a real YYYY-MM-DD Monday.
function isMonday(date: string): boolean {
  try {
    return weekOf(date) === date
  } catch {
    return false
  }
}
