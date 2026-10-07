import { useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, Plus } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import { useJob } from '../api/index.ts'
import { Confirm } from '../settings/LeaveGuard.tsx'
import { formatClockTime, useHouseholdSettings } from '../settings/api.ts'
import { Button } from '../ui/Button.tsx'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import {
  DRAFT_JOB_KIND,
  parseDraftResult,
  planKey,
  usePlan,
  useStartDraft,
  useSwapDay,
  type DraftResult,
  type PlanDay,
  type PlanStatus,
  type Swap,
} from './api.ts'
import { SwapSheet } from './SwapSheet.tsx'
import { addDays, defaultWeek, localNow, weekdayKicker, weekdayName, weekLabel } from './week.ts'
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
  internal_error: 'Something went wrong on the server.',
}

// PUSH_HINT is why Push to Calendar is unavailable.
// TODO(Feature 16): connect Push to Calendar once Google Calendar is.
const PUSH_HINT = 'Push to Calendar arrives once Google Calendar can be connected.'

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

// SkeletonTiles stands in for the seven days while the plan loads or the AI
// drafts it.
function SkeletonTiles({ label, children }: { label: string; children?: string }) {
  return (
    <section aria-busy="true" aria-label={label} className={styles.loading}>
      <div className={styles.grid}>
        {Array.from({ length: 7 }, (_, i) => (
          <Skeleton key={i} data-testid="skeleton-tile" className={styles.skeletonTile} />
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
function WeekBoard({ weekStart, timezone, onWeek }: { weekStart: string; timezone: string; onWeek: (weekStart: string) => void }) {
  const queryClient = useQueryClient()
  const plan = usePlan(weekStart)
  const startDraft = useStartDraft()
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob<unknown>(DRAFT_JOB_KIND, jobId)
  const [confirming, setConfirming] = useState(false)
  const swap = useSwapDay(weekStart)
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
  // plan changed meanwhile.
  const [readJobId, setReadJobId] = useState<string | null>(null)
  const ended = job.status === 'succeeded' || job.status === 'failed'
  useEffect(() => {
    if (!ended) return
    let active = true
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

  const { mutate } = startDraft
  function start(confirm: boolean) {
    setConfirming(false)
    // A draft asked for again leaves the last one's failure, and a failed
    // swap's, behind.
    setJobId(null)
    swap.reset()
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
  function rebuild() {
    if (plan.data && pushedToCalendar(plan.data.status)) setConfirming(true)
    else start(false)
  }

  const failure = swap.isError ? (
    <ErrorBanner onRetry={() => swapIn(swap.variables)}>
      Couldn't swap {weekdayName(swap.variables.date)}'s dinner. {swap.error.message}
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
    swap.mutate(chosen)
  }
  // openSwap opens the swap sheet on a day. One swap is sent at a time: the
  // plan the server answers it with must not overwrite another still pending.
  function openSwap(date: string) {
    if (swap.isPending) return
    swap.reset()
    setSwappingDate(date)
  }

  let body
  if (plan.isError) {
    body = <ErrorBanner onRetry={() => void plan.refetch()}>Couldn't load this week's plan. {plan.error.message}</ErrorBanner>
  } else if (drafting) {
    body = <SkeletonTiles label="Drafting this week">Drafting your week…</SkeletonTiles>
  } else if (resultError !== null) {
    // An unread draft shows no board: the plan read is not the draft.
    body = null
  } else if (plan.isPending) {
    body = <SkeletonTiles label="Plan loading" />
  } else if (plan.data === null) {
    body = (
      <EmptyState title="Nothing planned yet" action={{ label: 'Draft this week', onClick: rebuild }}>
        I'll draft seven dinners from your Regulars and Quest log.
      </EmptyState>
    )
  } else {
    const { ai_note, days } = plan.data
    const swappingDay = days.find((day) => day.date === swappingDate)
    body = (
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
            <button type="button" className={styles.rebuild} onClick={rebuild}>
              <Icon icon={Plus} size="inline" />
              Rebuild week
            </button>
          </li>
        </ul>
        <PushToCalendar />
        {swappingDay !== undefined && (
          <SwapSheet
            day={swappingDay}
            planned={new Set(days.map((day) => day.dinner.id))}
            onSwap={swapIn}
            onClose={() => setSwappingDate(null)}
          />
        )}
      </>
    )
  }

  const tag = plan.data ? statusTags[plan.data.status] : null
  return (
    <>
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

// PushToCalendar is "Looks good — push to Calendar", unavailable until
// Google Calendar can be connected: it stays focusable, so a tap or focus
// shows the tooltip saying why.
function PushToCalendar() {
  const hintId = useId()
  return (
    <div className={styles.push}>
      <Button className={styles.pushButton} aria-disabled="true" aria-describedby={hintId}>
        Looks good — push to Calendar
      </Button>
      <span id={hintId} role="tooltip" className={styles.hint}>
        {PUSH_HINT}
      </span>
    </div>
  )
}

// PlanPage is mock 1f, the mobile Draft board, on the week chosen with the
// previous / next week buttons. It opens on next week once this week's
// plan_draft time has come in the household's zone, otherwise on this week.
export function PlanPage() {
  const settings = useHouseholdSettings()
  const [week, setWeek] = useState<string | null>(null)
  if (week === null && settings.data) setWeek(defaultWeek(settings.data, new Date()))

  let content
  if (settings.isError) {
    content = (
      <ErrorBanner onRetry={() => void settings.refetch()}>Couldn't load the household settings. {settings.error.message}</ErrorBanner>
    )
  } else if (week === null || !settings.data) {
    content = <SkeletonTiles label="Plan loading" />
  } else {
    content = <WeekBoard key={week} weekStart={week} timezone={settings.data.timezone} onWeek={setWeek} />
  }
  return <div className={styles.page}>{content}</div>
}
