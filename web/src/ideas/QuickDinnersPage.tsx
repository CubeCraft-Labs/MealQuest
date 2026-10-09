import { Check, Plus, RefreshCw, Zap } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useIsMutating } from '@tanstack/react-query'
import { useJob } from '../api/index.ts'
import { DIFFICULTY_LABEL } from '../discover/DeckCard.tsx'
import { duration } from '../plan/PlanPage.tsx'
import { methodLabels } from '../settings/RegularsSettingsPage.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Segmented } from '../ui/Segmented.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import {
  ADDED_LABEL,
  ideasJobFailure,
  ideasJobLost,
  MAX_EXCLUDE,
  parseQuickResult,
  QUICK_IDEAS_KEPT,
  QUICK_JOB_KIND,
  quickTabKey,
  shownNames,
  useAddIdeaToQuestLog,
  useFindQuickIdeas,
  useQuickTab,
  type AddedDinner,
  type AddedStatus,
  type IdeaRef,
  type QuickIdea,
  type QuickKind,
} from './api.ts'
import styles from './QuickDinnersPage.module.css'

const TABS = [
  { value: 'crockpot', label: 'Crock pot' },
  { value: 'under30', label: '30 min or less' },
] as const satisfies readonly { value: QuickKind; label: string }[]

// helpers says what each tab finds.
const helpers: Record<QuickKind, string> = {
  crockpot: 'Set it going in the crock pot and come back to dinner, picked from your tastes.',
  under30: 'Prep to table in 30 minutes or less, picked from your tastes.',
}

// The server's code for a search that found nothing: on a later page, it
// means the tab has shown every idea there is.
const NO_IDEAS_FOUND = 'no_ideas_found'

// meta is a row's "25 min · Medium" line: "Crock pot" first for a crock-pot
// idea, and the time only when the recipe says it.
function meta(idea: QuickIdea): string {
  return [
    idea.method === 'crockpot' && methodLabels.crockpot,
    idea.minutes_total !== null && duration(idea.minutes_total),
    DIFFICULTY_LABEL[idea.difficulty],
  ]
    .filter((part): part is string => typeof part === 'string')
    .join(' · ')
}

// QuickRow is one idea (mock 1j): its washed photo, name and meta line, and
// the teal "+" that adds it to the Quest log, then a check labelled with
// where it went: added is the status of the dinner it came back as.
// ideaRef is null while its job runs: the server takes an idea only once
// the job has succeeded. onAdded marks the idea added, also if the row has
// unmounted before the add settles.
function QuickRow({
  idea,
  ideaRef,
  added,
  onAdded,
}: {
  idea: QuickIdea
  ideaRef: IdeaRef | null
  added: AddedStatus | undefined
  onAdded: (idea: IdeaRef, dinner: AddedDinner) => void
}) {
  const questLog = useAddIdeaToQuestLog(onAdded)
  return (
    <li className={styles.row} aria-label={idea.name}>
      <Photo id={idea.photo_id} name={idea.name} radius="50%" sizes="64px" className={styles.photo} />
      <div className={styles.text}>
        <h2 className={styles.name}>{idea.name}</h2>
        <span className={styles.meta}>{meta(idea)}</span>
        {questLog.isError && (
          <p className={styles.failure} role="alert">
            Couldn't add it to the Quest log. {questLog.error.message}
          </p>
        )}
      </div>
      {added !== undefined ? (
        <span className={styles.added} role="status">
          <Icon icon={Check} size="nav" label={ADDED_LABEL[added]} />
        </span>
      ) : (
        <IconButton
          label={`Add ${idea.name} to the Quest log`}
          tone="teal"
          className={styles.add}
          disabled={ideaRef === null || questLog.isPending}
          onClick={() => questLog.mutate(ideaRef!)}
        >
          <Icon icon={Plus} size="nav" />
        </IconButton>
      )}
    </li>
  )
}

// Skeletons stand in for the count ideas still to come.
function Skeletons({ count }: { count: number }) {
  return Array.from({ length: count }, (_, i) => (
    <li key={i} aria-hidden="true">
      <Skeleton data-testid="skeleton-row" className={styles.skeletonRow} />
    </li>
  ))
}

// QuickTabPanel is one tab's ideas: its pages so far, the page being
// found, filling as the server grounds it, and Show me more, which asks for
// a page excluding every name shown. The tab's first page is asked for when
// the tab is first shown; what it finds is kept for the session.
function QuickTabPanel({ kind }: { kind: QuickKind }) {
  const [tab, update] = useQuickTab(kind)
  const { mutate } = useFindQuickIdeas(kind)
  // A page being asked for, also by this tab before it was last shown.
  const starting = useIsMutating({ mutationKey: quickTabKey(kind) }) > 0
  const job = useJob<unknown>(QUICK_JOB_KIND, tab.jobId)
  const names = shownNames(tab)

  // ask asks for the next page, excluding every name shown; Retry asks
  // again for the page that failed. A page that could not start stays the
  // tab's startError until Retry: nothing asks again by itself.
  const ask = () => mutate(names)

  // The first page, asked for once.
  const asked = useRef(false)
  const fresh = tab.pages.length === 0 && tab.jobId === null && !tab.ended && tab.startError === null && !starting
  useEffect(() => {
    if (!fresh || asked.current) return
    asked.current = true
    mutate([])
  }, [fresh, mutate])

  // The job's result as far as it has gone, or why it cannot be read.
  const found = useMemo(() => {
    if (job.result === null || tab.jobId === null) return { result: null, error: null }
    try {
      return { result: parseQuickResult(`GET /api/v1/jobs/${tab.jobId}`, kind, job.result), error: null }
    } catch (error) {
      return { result: null, error: error as Error }
    }
  }, [job.result, tab.jobId, kind])

  const failure = job.status === 'failed' ? job.error!.message : null
  // A later page that finds nothing means there is nothing more to show.
  const exhausted = failure === NO_IDEAS_FOUND && tab.pages.length > 0

  // A page found is kept with the tab's pages; a page that ends the tab
  // ends it.
  const jobId = tab.jobId
  const ideas = job.status === 'succeeded' ? (found.result?.ideas ?? null) : null
  useEffect(() => {
    if (jobId === null) return
    if (ideas !== null) update((t) => (t.jobId === jobId ? { ...t, pages: [...t.pages, { jobId, ideas }], jobId: null } : t))
    else if (exhausted) update((t) => (t.jobId === jobId ? { ...t, jobId: null, ended: true } : t))
  }, [jobId, ideas, exhausted, update])

  const markAdded = ({ jobId, index }: IdeaRef, { status }: AddedDinner) =>
    update((t) => ({ ...t, added: { ...t.added, [`${jobId}:${index}`]: status } }))

  // The rows of every page found, then of the page being found, in one
  // keyed list: a page kept with the tab's pages keeps its rows, and with
  // them any add still under way.
  const rows: { idea: QuickIdea; ref: IdeaRef | null; key: string }[] = tab.pages.flatMap((page) =>
    page.ideas.map((idea, index) => ({ idea, ref: { jobId: page.jobId, index }, key: `${page.jobId}:${index}` })),
  )

  let next = null
  if (starting) {
    next = <Skeletons count={QUICK_IDEAS_KEPT} />
  } else if (tab.startError !== null) {
    next = (
      <li>
        <ErrorBanner onRetry={ask}>Couldn't start looking for Quick dinners. {tab.startError.message}</ErrorBanner>
      </li>
    )
  } else if (jobId !== null && failure !== null && !exhausted) {
    next = (
      <li>
        <ErrorBanner onRetry={ask}>Couldn't find Quick dinners. {ideasJobFailure(failure)}</ErrorBanner>
      </li>
    )
  } else if (found.error !== null) {
    next = (
      <li>
        <ErrorBanner onRetry={ask}>Couldn't read the Quick dinners. {found.error.message}</ErrorBanner>
      </li>
    )
  } else if (jobId !== null && !exhausted) {
    const shown = found.result?.ideas ?? []
    const done = job.status === 'succeeded'
    shown.forEach((idea, index) => rows.push({ idea, ref: done ? { jobId, index } : null, key: `${jobId}:${index}` }))
    // A job not found will not be found by asking again: Retry asks for the
    // page again. Any other failed poll is asked again.
    const lost = ideasJobLost(job.error)
    next = (
      <>
        {!done && <Skeletons count={Math.max(QUICK_IDEAS_KEPT - shown.length, 0)} />}
        {job.error !== null && (
          <li>
            <ErrorBanner onRetry={lost ? ask : job.poll}>Couldn't check on the Quick dinners. {job.error.message}</ErrorBanner>
          </li>
        )}
      </>
    )
  }

  // Show me more once the tab is between pages; the server takes at most
  // MAX_EXCLUDE names to leave out, so past that there is no more to ask.
  const between = !starting && tab.startError === null && jobId === null && tab.pages.length > 0
  const allShown = tab.ended || names.length > MAX_EXCLUDE
  const busy = starting || (jobId !== null && failure === null && found.error === null && job.status !== 'succeeded')

  return (
    <section aria-label={TABS.find((t) => t.value === kind)!.label} className={styles.panel}>
      <p className={styles.helper}>{helpers[kind]}</p>
      <ul aria-busy={busy} aria-label="Quick dinners" className={styles.rows}>
        {rows.map(({ idea, ref, key }) => (
          <QuickRow key={key} idea={idea} ideaRef={ref} added={tab.added[key]} onAdded={markAdded} />
        ))}
        {next}
      </ul>
      {between &&
        (allShown ? (
          <p className={styles.end} role="status">
            That's every Quick dinner for now.
          </p>
        ) : (
          <button type="button" className={styles.more} onClick={ask}>
            <Icon icon={RefreshCw} /> Show me more
          </button>
        ))}
    </section>
  )
}

// QuickDinnersPage is mock 1j, Quick dinners: a Crock pot | 30 min or less
// tab for each kind of Quick dinner, each a quick_ideas search of its own
// whose ideas fill as the server grounds them, kept for the session.
export function QuickDinnersPage() {
  const [kind, setKind] = useState<QuickKind>('under30')
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <span className={styles.zap}>
          <Icon icon={Zap} className={styles.zapIcon} />
        </span>
        <h1 className={styles.title}>Quick dinners</h1>
      </header>
      <Segmented label="Kind of Quick dinner" options={TABS} value={kind} onValueChange={setKind} className={styles.tabs} />
      <QuickTabPanel key={kind} kind={kind} />
    </div>
  )
}
