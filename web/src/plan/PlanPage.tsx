import { useQueryClient } from '@tanstack/react-query'
import { CalendarDays, Check, ChevronLeft, ChevronRight, Plus, RefreshCw, ShoppingCart } from 'lucide-react'
import { useEffect, useId, useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { ApiError, useJob } from '../api/index.ts'
import { QUEST_LOG_KEY } from '../discover/api.ts'
import { Confirm } from '../settings/LeaveGuard.tsx'
import { methodLabels } from '../settings/RegularsSettingsPage.tsx'
import { formatClockTime, useHouseholdSettings } from '../settings/api.ts'
import { useBreakpoint, type Breakpoint } from '../shell/useBreakpoint.ts'
import { Button, type ButtonVariant } from '../ui/Button.tsx'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import {
  DRAFT_JOB_KIND,
  parseDraftResult,
  planKey,
  usePlan,
  usePushToCalendar,
  useStartDraft,
  useSwapDay,
  type DraftResult,
  type Plan,
  type PlanDay,
  type PlanDinner,
  type PlanStatus,
  type Swap,
} from './api.ts'
import { DragAnnouncer, DROP_DATE, DropBoard, PlanDragProvider, useDropTarget } from './drag.tsx'
import { PlanPanel, QuestLogStrip } from './PlanPanel.tsx'
import { SwapSheet } from './SwapSheet.tsx'
import {
  addDays,
  dayOfMonth,
  defaultWeek,
  localNow,
  weekdayKicker,
  weekdayName,
  weekLabel,
  weekStart as weekOf,
} from './week.ts'
import styles from './PlanPage.module.css'

// statusTags is the tag beside the week's title for each plan status.
const statusTags: Record<PlanStatus, { label: string; tone: 'grape' | 'teal' }> = {
  draft: { label: 'Draft', tone: 'grape' },
  synced: { label: 'Synced', tone: 'teal' },
  changed: { label: 'Changes not pushed', tone: 'grape' },
}

// draftFailures says why a draft_week job failed, by its failure code. Any
// other code is shown as it is.
const draftFailures: Record<string, string> = {
  not_enough_dinners: 'There are fewer than seven Regulars and Quest-log dinners to draft from.',
  ai_failed: "The AI assistant couldn't draft it.",
  confirm_required: 'The plan was pushed to Calendar while it was drafting.',
  plan_changed: 'The plan was edited while it was drafting.',
  pool_changed: 'A dinner it picked was archived, tried or planned for another week while it was drafting.',
  internal_error: 'Something went wrong on the server.',
}

// CALENDAR_SETTINGS is the Google Calendar settings screen.
const CALENDAR_SETTINGS = '/settings/calendar'

// settingsFailures says why a push failed, by its API error code, for the
// failures only the Google Calendar settings can fix, and what to do there.
const settingsFailures: Record<string, { reason: string; fix: string }> = {
  google_reconnect_required: { reason: 'Google access was revoked or expired.', fix: 'Reconnect Google Calendar' },
  not_connected: { reason: 'Google Calendar is not connected.', fix: 'Connect Google Calendar' },
  no_calendar: { reason: 'No calendar is chosen for dinners yet.', fix: 'Choose a calendar in Settings' },
  calendar_unavailable: {
    reason: 'The chosen calendar was deleted or MealQuest can no longer add dinners to it.',
    fix: 'Choose another calendar',
  },
}

// SHOPPING_HINT is why the shopping list is unavailable.
// TODO(Feature 17): open the Supply run, with its count, once it is built.
const SHOPPING_HINT = 'The shopping list arrives once the Supply run is built.'

// syncStatuses is the plan manager's line on whether the plan is on Calendar.
const syncStatuses: Record<PlanStatus, string> = {
  draft: 'Not pushed to Google Calendar yet',
  synced: 'Synced to Google Calendar',
  changed: 'Changes not pushed to Google Calendar',
}

// duration is minutes as the plan manager's cards say it: "45 min", "6 hr",
// "1 hr 30 min", with no-break spaces so a narrow card wraps the line
// between the method and the duration, never inside the duration.
export function duration(minutes: number): string {
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  if (hours === 0) return `${rest}\u00a0min`
  return rest === 0 ? `${hours}\u00a0hr` : `${hours}\u00a0hr\u00a0${rest}\u00a0min`
}

// cooking is a card's "method · duration" line, "Crock pot · 6 hr", with
// whichever of the two is known, or null when neither is.
export function cooking(dinner: PlanDinner): string | null {
  const parts = [dinner.method && methodLabels[dinner.method], dinner.minutes !== null && duration(dinner.minutes)].filter(
    (part): part is string => typeof part === 'string',
  )
  return parts.length === 0 ? null : parts.join(' · ')
}

// ago is how long before now the instant at was: "just now", "2 min ago",
// "3 hr ago", "2 days ago".
export function ago(at: Date, now: Date): string {
  const minutes = Math.floor((now.getTime() - at.getTime()) / 60_000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} hr ago`
  const days = Math.floor(hours / 24)
  return days === 1 ? '1 day ago' : `${days} days ago`
}

// pushedToCalendar says whether drafting over a plan of this status needs
// confirm: it was pushed to Calendar.
function pushedToCalendar(status: PlanStatus): boolean {
  return status !== 'draft'
}

// DayTile is one day of the Draft board: a Regular on the surface, a
// Quest-log pick in teal with its "NEW" kicker, and a crock-pot dinner's
// start-by time in the household's zone. Tapping it opens the swap sheet;
// while that is open the tile is swapping: on the page background, ringed in
// grape, its name muted.
function DayTile({ day, timezone, swapping, onSwap }: { day: PlanDay; timezone: string; swapping: boolean; onSwap: () => void }) {
  const kicker = weekdayKicker(day.date)
  const classes = [styles.tile, day.is_new && styles.newTile, swapping && styles.swapping].filter(Boolean).join(' ')
  return (
    <li className={classes}>
      <button type="button" className={styles.tileButton} onClick={onSwap}>
        <span className={styles.dayKicker}>{swapping ? `${kicker} · SWAPPING…` : day.is_new ? `${kicker} · NEW` : kicker}</span>
        <span>
          <span className={styles.dinner}>{day.dinner.name}</span>
          {day.start_by !== null && (
            <span className={styles.startBy}>Start by {formatClockTime(localNow(timezone, new Date(day.start_by)).time)}</span>
          )}
        </span>
      </button>
    </li>
  )
}

// DayColumn is one day of the plan manager's 7-column grid: its day label
// over a card with the dinner's washed photo, name, method and time, a
// crock-pot dinner's start-by time in the household's zone, and its Regular
// or Side quest tag, a Side quest's card in teal. The swap icon opens the swap
// sheet; while that is open the card is swapping, ringed in grape. A dinner
// dragged over the day drops on it: the card is swapping, covered by a
// dashed grape "Drop to swap" saying which dinner it replaces.
function DayColumn({ day, timezone, swapping, onSwap }: { day: PlanDay; timezone: string; swapping: boolean; onSwap: () => void }) {
  const { dinner } = day
  const time = cooking(dinner)
  const target = useDropTarget(day.date) !== null
  const classes = [styles.card, day.is_new && styles.sideQuestCard, (swapping || target) && styles.swapping].filter(Boolean).join(' ')
  return (
    <li className={styles.column} aria-label={weekdayName(day.date)} {...{ [DROP_DATE]: day.date }}>
      <p className={styles.dayLabel} aria-hidden="true">
        <span className={styles.dayName}>{weekdayKicker(day.date)}</span>
        <span className={styles.dayNumber}>{dayOfMonth(day.date)}</span>
      </p>
      <div className={classes}>
        {/* The card's name already says what the photo shows. */}
        <span aria-hidden="true">
          <Photo id={dinner.photo_id} name={dinner.name} radius="18px" sizes="160px" className={styles.cardPhoto} />
        </span>
        <p className={styles.cardName}>{dinner.name}</p>
        {time !== null && <p className={styles.cardMinutes}>{time}</p>}
        {day.start_by !== null && (
          <p className={styles.cardStartBy}>Start by {formatClockTime(localNow(timezone, new Date(day.start_by)).time)}</p>
        )}
        <div className={styles.cardFooter}>
          <Tag tone={day.is_new ? 'grape' : 'neutral'} className={styles.cardTag}>{day.is_new ? 'Side quest' : 'Regular'}</Tag>
          <IconButton label={`Swap ${weekdayName(day.date)}`} className={styles.swapButton} onClick={onSwap}>
            <Icon icon={RefreshCw} size="inline" />
          </IconButton>
        </div>
        {target && (
          <div className={styles.dropTarget}>
            <p className={styles.dropTitle}>Drop to swap</p>
            <p className={styles.dropReplaces}>Replaces {dinner.name}</p>
          </div>
        )}
      </div>
    </li>
  )
}

// SkeletonTiles stands in for the seven days while the plan loads or the AI
// drafts it, as two columns of tiles on mobile and seven columns wider.
function SkeletonTiles({ label, layout, children }: { label: string; layout: Breakpoint; children?: string }) {
  const wide = layout !== 'mobile'
  return (
    <section aria-busy="true" aria-label={label} className={styles.loading}>
      <div className={wide ? `${styles.week} ${styles[layout]}` : styles.grid}>
        {Array.from({ length: 7 }, (_, i) => (
          <Skeleton key={i} data-testid="skeleton-tile" className={wide ? styles.skeletonCard : styles.skeletonTile} />
        ))}
      </div>
      {children !== undefined && <p className={styles.drafting}>{children}</p>}
    </section>
  )
}

// WeekBoard is the Draft board of the week starting on weekStart. Drafting
// it — the empty state's "Draft this week" or the "+ Rebuild week" tile —
// starts the draft_week job and shows skeleton tiles until the job ends and
// the plan is read again. Rebuilding a plan pushed to Calendar asks first,
// and sends confirm. A failed draft is shown with Retry; no plan is made up.
//
// From tablet up it is the plan manager (mocks 2c, 2d): Rebuild week, Push to
// Calendar and the unavailable Shopping list are actions in the header,
// over the plan's sync status and the 7-column grid.
//
// Push to Calendar, on either layout, sends the plan shown and shows it as
// synced once the server answers; a failure says why, linking to the Google
// Calendar settings when only they can fix it.
function WeekBoard({
  weekStart,
  timezone,
  layout,
  onWeek,
}: {
  weekStart: string
  timezone: string
  layout: Breakpoint
  onWeek: (weekStart: string) => void
}) {
  const queryClient = useQueryClient()
  const plan = usePlan(weekStart)
  const startDraft = useStartDraft()
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob<unknown>(DRAFT_JOB_KIND, jobId)
  const [confirming, setConfirming] = useState(false)
  const swap = useSwapDay(weekStart)
  const push = usePushToCalendar(weekStart)
  const pushing = push.pending
  // The date of the day being swapped, while the swap sheet is open.
  const [swappingDate, setSwappingDate] = useState<string | null>(null)
  const label = weekLabel(weekStart)

  // The draft's result, once the job has succeeded, or why it cannot be read.
  const result = useMemo((): { draft: DraftResult | null; error: Error | null } => {
    if (job.status !== 'succeeded') return { draft: null, error: null }
    try {
      return { draft: parseDraftResult(`GET /api/v1/jobs/${jobId}`, weekStart, job.result), error: null }
    } catch (error) {
      return { draft: null, error: error as Error }
    }
  }, [job.status, job.result, jobId, weekStart])

  // Once the job ends the plan is read again: a succeeded draft is followed
  // until that read lands; a failed one stays shown, and may have met a
  // plan changed meanwhile. The Quest log is read again too, as after a
  // swap: the draft may have planned some of it.
  const [readJobId, setReadJobId] = useState<string | null>(null)
  const ended = job.status === 'succeeded' || job.status === 'failed'
  useEffect(() => {
    if (!ended) return
    let active = true
    void queryClient.invalidateQueries({ queryKey: QUEST_LOG_KEY })
    void queryClient.invalidateQueries({ queryKey: planKey(weekStart) }).then(() => {
      if (active) setReadJobId(jobId)
    })
    return () => {
      active = false
    }
  }, [queryClient, ended, jobId, weekStart])

  // The plan read shows once it is the one drafted, read from the server; no
  // plan, another one, or a copy cached before the draft (a redraft keeps the
  // plan's id) is an error rather than an empty or stale board. Its Retry
  // reads the plan again, keeping the job: a later read from the server that
  // is the draft still lands it.
  const { draft } = result
  const read = draft !== null && readJobId === jobId && plan.isSuccess
  const landed = read && plan.data?.id === draft.plan_id && !plan.data.cached
  const unreadError =
    read && !landed
      ? new Error(
          plan.data === null
            ? `The week has no plan, not draft ${draft.plan_id}.`
            : plan.data.id !== draft.plan_id
              ? `The week's plan is not draft ${draft.plan_id}.`
              : `The plan came from this device's cache, not the server, so it may not be draft ${draft.plan_id} yet.`,
        )
      : null
  const resultError = result.error ?? unreadError
  useEffect(() => {
    if (landed) setJobId(null)
  }, [landed])

  const drafting = startDraft.isPending || (jobId !== null && job.status !== 'failed' && resultError === null)

  // A push sends the plan shown: it waits for a draft or a swap of the week
  // to settle, and neither starts while it runs, even across leaving the
  // week and coming back. Nor does it send a plan from this device's cache,
  // which may not be the server's: after a push refused because the plan
  // changed, the read of the changed plan may be answered from the cache.
  const cached = plan.isSuccess && plan.data !== null && plan.data.cached
  const canPush = plan.isSuccess && plan.data !== null && !cached && !drafting && !swap.pending && !pushing
  function pushToCalendar() {
    if (!canPush) return
    swap.dismiss()
    push.reset()
    push.mutate()
  }

  const { mutate } = startDraft
  function start(confirm: boolean) {
    setConfirming(false)
    // A draft asked for again leaves the last one's failure, a failed
    // swap's and a failed push's behind.
    setJobId(null)
    swap.dismiss()
    push.reset()
    mutate(
      { weekStart, confirm },
      {
        onSuccess: setJobId,
        // A plan pushed to Calendar meanwhile needs confirm: read it again.
        onError: () => void queryClient.invalidateQueries({ queryKey: planKey(weekStart) }),
      },
    )
  }
  // rebuild drafts the week, asking first over a plan pushed to Calendar.
  // It waits for a swap of the week still pending, which may yet fail: the
  // Rebuild week tile is disabled meanwhile.
  function rebuild() {
    if (swap.pending || pushing) return
    if (plan.data && pushedToCalendar(plan.data.status)) setConfirming(true)
    else start(false)
  }

  const failure = push.error !== null ? (
    <PushFailure error={push.error} cached={cached} onRetry={cached ? () => void plan.refetch() : pushToCalendar} />
  ) : swap.failed !== null ? (
    <ErrorBanner onRetry={() => swapIn(swap.failed!.swap)}>
      Couldn't swap {weekdayName(swap.failed.swap.date)}'s dinner. {swap.failed.error.message}
    </ErrorBanner>
  ) : startDraft.isError ? (
    <ErrorBanner onRetry={rebuild}>Couldn't start drafting this week. {startDraft.error.message}</ErrorBanner>
  ) : result.error !== null ? (
    <ErrorBanner onRetry={rebuild}>Couldn't read the draft. {result.error.message}</ErrorBanner>
  ) : unreadError !== null ? (
    <ErrorBanner onRetry={() => void plan.refetch()}>Couldn't read the draft. {unreadError.message}</ErrorBanner>
  ) : job.error === null ? null : job.status === 'failed' ? (
    <ErrorBanner onRetry={rebuild}>
      Couldn't draft this week. {draftFailures[job.error.message] ?? job.error.message}
    </ErrorBanner>
  ) : (
    <ErrorBanner onRetry={job.poll}>Couldn't check on the draft. {job.error.message}</ErrorBanner>
  )

  // swapIn sends the swap chosen in the sheet and closes it.
  function swapIn(chosen: Swap) {
    setSwappingDate(null)
    push.reset()
    swap.mutate(chosen)
  }
  // dropIn swaps in a dinner dragged onto a day. A drag is not picked up,
  // nor dropped, while a swap of the week is pending.
  function dropIn(chosen: Swap) {
    swap.dismiss()
    swapIn(chosen)
  }
  // openSwap opens the swap sheet on a day. One swap of the week is sent at
  // a time, even across leaving the week and coming back: the plan the
  // server answers it with must not overwrite another still pending. Nor is
  // one sent while the week is being pushed to Calendar.
  function openSwap(date: string) {
    if (swap.pending || pushing) return
    swap.dismiss()
    setSwappingDate(date)
  }

  let body
  if (plan.isError) {
    body = <ErrorBanner onRetry={() => void plan.refetch()}>Couldn't load this week's plan. {plan.error.message}</ErrorBanner>
  } else if (drafting) {
    body = (
      <SkeletonTiles label="Drafting this week" layout={layout}>
        Drafting your week…
      </SkeletonTiles>
    )
  } else if (resultError !== null) {
    // An unread draft shows no board: the plan read is not the draft.
    body = null
  } else if (plan.isPending) {
    body = <SkeletonTiles label="Plan loading" layout={layout} />
  } else if (plan.data === null) {
    body = (
      <EmptyState title="Nothing planned yet" action={{ label: 'Draft this week', onClick: rebuild }}>
        I'll draft seven dinners from your Regulars and Quest log.
      </EmptyState>
    )
  } else {
    const { ai_note, days } = plan.data
    const swappingDay = days.find((day) => day.date === swappingDate)
    const sheet = swappingDay !== undefined && (
      <SwapSheet
        day={swappingDay}
        planned={new Set(days.map((day) => day.dinner.id))}
        onSwap={swapIn}
        onClose={() => setSwappingDate(null)}
      />
    )
    body =
      layout !== 'mobile' ? (
        <>
          <DropBoard days={days} pending={swap.pending || pushing} drop={dropIn} />
          <ul className={`${styles.week} ${styles[layout]}`} aria-label="Dinners this week">
            {days.map((day) => (
              <DayColumn
                key={day.date}
                day={day}
                timezone={timezone}
                swapping={day.date === swappingDate}
                onSwap={() => openSwap(day.date)}
              />
            ))}
          </ul>
          {sheet}
        </>
      ) : (
        <>
          {ai_note !== null && (
            <div className={styles.assistant}>
              <span className={styles.avatar} aria-hidden="true">
                Q
              </span>
              <p className={styles.bubble}>{ai_note}</p>
            </div>
          )}
          <ul className={styles.grid} aria-label="Dinners this week">
            {days.map((day) => (
              <DayTile
                key={day.date}
                day={day}
                timezone={timezone}
                swapping={day.date === swappingDate}
                onSwap={() => openSwap(day.date)}
              />
            ))}
            <li className={styles.rebuildCell}>
              <button type="button" className={styles.rebuild} onClick={rebuild} disabled={swap.pending || pushing}>
                <Icon icon={Plus} size="inline" />
                Rebuild week
              </button>
            </li>
          </ul>
          <Button className={styles.pushButton} onClick={pushToCalendar} disabled={!canPush} aria-busy={pushing}>
            {pushing ? 'Pushing to Calendar…' : 'Looks good — push to Calendar'}
          </Button>
          <SyncStatus plan={plan.data} className={styles.mobileStatus} />
          {sheet}
        </>
      )
  }

  const tag = plan.data ? statusTags[plan.data.status] : null
  const header =
    layout !== 'mobile' ? (
      <header className={styles.wideHeader}>
        <div className={styles.titleBar}>
          <div className={styles.heading}>
            <p className={styles.kicker}>{label.week} · Plan</p>
            <h1 className={styles.wideTitle}>{label.dates}</h1>
          </div>
          <div className={styles.weekNav}>
            <IconButton label="Previous week" onClick={() => onWeek(addDays(weekStart, -7))}>
              <Icon icon={ChevronLeft} size="inline" />
            </IconButton>
            {layout === 'desktop' && (
              <button type="button" className={styles.thisWeek} onClick={() => onWeek(weekOf(localNow(timezone, new Date()).date))}>
                This week
              </button>
            )}
            <IconButton label="Next week" onClick={() => onWeek(addDays(weekStart, 7))}>
              <Icon icon={ChevronRight} size="inline" />
            </IconButton>
          </div>
        </div>
        <div className={styles.actionBar}>
          {plan.data ? <SyncStatus plan={plan.data} /> : <span className={styles.status} />}
          <div className={styles.actions}>
            <Unavailable hint={SHOPPING_HINT} variant="outline" below className={styles.action}>
              <Icon icon={ShoppingCart} size="inline" />
              Shopping list
            </Unavailable>
            <Button
              variant="outline"
              className={styles.action}
              onClick={rebuild}
              disabled={swap.pending || pushing || drafting || !plan.isSuccess}
            >
              <Icon icon={RefreshCw} size="inline" />
              Rebuild week
            </Button>
            <Button className={styles.action} onClick={pushToCalendar} disabled={!canPush} aria-busy={pushing}>
              <Icon icon={CalendarDays} size="inline" />
              {pushing ? 'Pushing…' : 'Push to Calendar'}
            </Button>
          </div>
        </div>
      </header>
    ) : (
      <header className={styles.header}>
        <div className={styles.heading}>
          <p className={styles.kicker}>{label.week}</p>
          <div className={styles.titleRow}>
            <h1 className={styles.title}>{label.dates}</h1>
            {tag !== null && <Tag tone={tag.tone}>{tag.label}</Tag>}
          </div>
        </div>
        <div className={styles.weekNav}>
          <IconButton label="Previous week" onClick={() => onWeek(addDays(weekStart, -7))}>
            <Icon icon={ChevronLeft} size="inline" />
          </IconButton>
          <IconButton label="Next week" onClick={() => onWeek(addDays(weekStart, 7))}>
            <Icon icon={ChevronRight} size="inline" />
          </IconButton>
        </div>
      </header>
    )
  return (
    <>
      {header}
      {body}
      {failure}
      <Confirm
        open={confirming}
        title="Rebuild this week?"
        confirmLabel="Rebuild week"
        cancelLabel="Keep this plan"
        onCancel={() => setConfirming(false)}
        onConfirm={() => start(true)}
      >
        This week is already pushed to Calendar. The AI assistant drafts a new plan in its place.
      </Confirm>
    </>
  )
}

// SyncStatus is the plan's status line, in the plan manager's header and
// under the Draft board's push button: whether the plan is on Google
// Calendar and, once synced, how long ago it was pushed, kept current minute
// by minute.
function SyncStatus({ plan, className }: { plan: Plan; className?: string }) {
  const { status, calendar_synced_at: syncedAt } = plan
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), 60_000)
    return () => clearInterval(timer)
  }, [])
  return (
    <p className={[styles.status, status === 'synced' && styles.synced, className].filter(Boolean).join(' ')}>
      {status === 'synced' && <Icon icon={Check} size="inline" />}
      {syncStatuses[status]}
      {status === 'synced' && syncedAt !== null && ` ${ago(syncedAt, now)}`}
    </p>
  )
}

// PushFailure says why Push to Calendar failed. A failure only the Google
// Calendar settings can fix links there; any other offers to push again,
// or, while the plan shown is this device's cached copy, to read it again
// from the server first.
function PushFailure({ error, cached, onRetry }: { error: Error; cached: boolean; onRetry: () => void }) {
  const code = error instanceof ApiError ? error.code : null
  const settings = code !== null && Object.hasOwn(settingsFailures, code) ? settingsFailures[code] : undefined
  if (settings !== undefined) {
    return (
      <ErrorBanner>
        Couldn't push to Calendar. {settings.reason}{' '}
        <Link to={CALENDAR_SETTINGS} className={styles.settingsLink}>
          {settings.fix}
        </Link>
      </ErrorBanner>
    )
  }
  return (
    <ErrorBanner onRetry={onRetry}>
      Couldn't push to Calendar.{' '}
      {cached
        ? `${code === 'plan_changed' ? 'The plan changed while it was being pushed.' : error.message} The plan shown came from this device's cache, not the server; read it again before pushing.`
        : code === 'plan_changed'
          ? 'The plan changed while it was being pushed; push it again.'
          : error.message}
    </ErrorBanner>
  )
}

// Unavailable is an action with nothing behind it yet (the shopping list):
// it stays focusable, so a tap or focus shows the tooltip saying why, above
// it or, at the top of the page, below.
function Unavailable({
  hint,
  variant = 'primary',
  below = false,
  className,
  children,
}: {
  hint: string
  variant?: ButtonVariant
  below?: boolean
  className?: string
  children: ReactNode
}) {
  const hintId = useId()
  return (
    <div className={[styles.unavailable, below && styles.below].filter(Boolean).join(' ')}>
      <Button
        variant={variant}
        className={[className, styles[variant]].filter(Boolean).join(' ')}
        aria-disabled="true"
        aria-describedby={hintId}
      >
        {children}
      </Button>
      <span id={hintId} role="tooltip" className={styles.hint}>
        {hint}
      </span>
    </div>
  )
}

// PlanPage is the Draft board on the week chosen with the previous / next
// week buttons: mock 1f on mobile; from tablet up the plan manager, with the
// Quest log as a strip under the grid on tablet (2c) and the Quest log and
// Regulars in a right panel on desktop (2d). It opens on next week once this
// week's plan_draft time has come in the household's zone, otherwise on this
// week. The board keeps its place in the tree across layouts, so crossing a
// breakpoint keeps a draft being followed. From tablet up a dinner is
// dragged from the strip or the panel onto a day of the grid to swap it in.
export function PlanPage() {
  const layout = useBreakpoint()
  const settings = useHouseholdSettings()
  const [week, setWeek] = useState<string | null>(null)
  if (week === null && settings.data) setWeek(defaultWeek(settings.data, new Date()))

  let content
  if (settings.isError) {
    content = (
      <ErrorBanner onRetry={() => void settings.refetch()}>Couldn't load the household settings. {settings.error.message}</ErrorBanner>
    )
  } else if (week === null || !settings.data) {
    content = <SkeletonTiles label="Plan loading" layout={layout} />
  } else {
    content = <WeekBoard key={week} weekStart={week} timezone={settings.data.timezone} layout={layout} onWeek={setWeek} />
  }
  return (
    <PlanDragProvider>
      <div className={`${styles.page} ${styles[`${layout}Page`]}`}>
        <div className={styles.board}>
          {content}
          {layout === 'tablet' && <QuestLogStrip />}
        </div>
        {layout === 'desktop' && <PlanPanel />}
        {layout !== 'mobile' && <DragAnnouncer />}
      </div>
    </PlanDragProvider>
  )
}
