import { useQueryClient } from '@tanstack/react-query'
import { Heart, X } from 'lucide-react'
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useJob } from '../api/index.ts'
import { Card } from '../ui/Card.tsx'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import {
  BATCH_JOB_KIND,
  DECK_KEY,
  parseBatchResult,
  useDeck,
  useDecide,
  useQuestLog,
  useStartBatch,
  type Choice,
} from './api.ts'
import { DeckCard, DECK_DEPTH } from './DeckCard.tsx'
import styles from './DiscoverPage.module.css'

// isEditable says whether a key press belongs to a field being typed in,
// which the deck's ← / → keys leave alone.
function isEditable(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLElement &&
    (target.isContentEditable || target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement)
  )
}

// QuestLogPill is the "Quest log · N" pill. It shows and hides the names
// of the dinners in the Quest log; the Quest log sheet replaces this list.
function QuestLogPill({ dinners }: { dinners: { id: string; name: string }[] }) {
  const [open, setOpen] = useState(false)
  const listId = useId()
  return (
    <>
      <button
        type="button"
        className={styles.questLogButton}
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        onClick={() => setOpen(!open)}
      >
        <span className={styles.questLog}>Quest log · {dinners.length}</span>
      </button>
      {open && (
        <Card id={listId} tone="muted" className={styles.questLogList}>
          {dinners.length === 0 ? (
            <p className={styles.questLogEmpty}>No dinners in the Quest log yet. Try it on a Side quest to add one.</p>
          ) : (
            <ul aria-label="Quest log" className={styles.questLogNames}>
              {dinners.map((dinner) => (
                <li key={dinner.id}>{dinner.name}</li>
              ))}
            </ul>
          )}
        </Card>
      )}
    </>
  )
}

// DiscoverPage is mock 1a: the deck of Side quests to Try it or Pass, one
// at a time from the top card. Every decision — the buttons, the ← / →
// keys, Retry — goes through submit, which does nothing while one is in
// flight. The lock is a ref, set before the decision is sent, so a second
// press before the next render cannot send another.
export function DiscoverPage() {
  const queryClient = useQueryClient()
  const deck = useDeck()
  const questLog = useQuestLog()
  const decision = useDecide()
  const startBatch = useStartBatch()
  const job = useJob<unknown>(BATCH_JOB_KIND, startBatch.data?.job_id ?? null)

  const top = deck.data?.[0]
  const busy = decision.isPending
  const jobRunning = startBatch.isSuccess && job.status !== 'succeeded' && job.status !== 'failed'
  const finding = startBatch.isPending || jobRunning

  // The batch's result as far as it has gone, or why it cannot be read.
  const batch = useMemo(() => {
    if (job.result === null) return { stored: 0, error: null }
    try {
      return { stored: parseBatchResult(`GET /api/v1/jobs/${startBatch.data?.job_id}`, job.result).suggestion_ids.length, error: null }
    } catch (error) {
      return { stored: 0, error: error as Error }
    }
  }, [job.result, startBatch.data?.job_id])
  // The job's failure, the latest poll's failure, an answer that is not the
  // batch's job, or an unreadable result.
  const batchError = job.error ?? batch.error

  const { mutate: mutateDecision } = decision
  const deciding = useRef(false)
  const submit = useCallback(
    (variables: { id: string; choice: Choice }) => {
      if (deciding.current) return
      deciding.current = true
      mutateDecision(variables, {
        onSettled: () => {
          deciding.current = false
        },
      })
    },
    [mutateDecision],
  )
  const decide = useCallback(
    (choice: Choice) => {
      if (top !== undefined) submit({ id: top.id, choice })
    },
    [top, submit],
  )

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
      if (isEditable(event.target)) return
      if (event.key === 'ArrowLeft') decide('pass')
      else if (event.key === 'ArrowRight') decide('try')
      else return
      event.preventDefault()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [decide])

  // The batch stores its Side quests as it goes, so the deck is read again
  // whenever it reports more, and once it ends.
  const { stored } = batch
  useEffect(() => {
    if (job.status === undefined) return
    void queryClient.invalidateQueries({ queryKey: DECK_KEY })
  }, [queryClient, job.status, stored])

  let body
  if (deck.isError) {
    body = <ErrorBanner onRetry={() => void deck.refetch()}>Couldn't load your side quests. {deck.error.message}</ErrorBanner>
  } else if (deck.isPending || (deck.data.length === 0 && finding)) {
    body = (
      <section aria-busy="true" aria-label="Side quests loading" className={styles.loading}>
        <div className={styles.deck}>
          {DECK_DEPTH.map((depth) => (
            <Skeleton key={depth} data-testid="skeleton-card" className={`${styles.skeletonCard} ${styles[`depth${depth}`]}`} />
          ))}
        </div>
        {finding && <p className={styles.finding}>Finding new side quests…</p>}
        {jobRunning && batchError !== null && (
          <ErrorBanner onRetry={job.poll}>Couldn't check on new side quests. {batchError.message}</ErrorBanner>
        )}
      </section>
    )
  } else if (top === undefined) {
    body = (
      <>
        {startBatch.isError && (
          <ErrorBanner onRetry={() => startBatch.mutate()}>Couldn't ask for more side quests. {startBatch.error.message}</ErrorBanner>
        )}
        {batchError !== null && (
          <ErrorBanner onRetry={() => startBatch.mutate()}>Couldn't find new side quests. {batchError.message}</ErrorBanner>
        )}
        <EmptyState title="All caught up" action={{ label: 'Get more now', onClick: () => startBatch.mutate() }}>
          New side quests arrive Sunday.
        </EmptyState>
      </>
    )
  } else {
    body = (
      <>
        <div className={styles.deck}>
          {deck.data.slice(0, DECK_DEPTH.length).map((quest, depth) => (
            <DeckCard key={quest.id} quest={quest} depth={depth} />
          ))}
        </div>
        <div className={styles.actions}>
          <IconButton label="Pass" size={62} tone="neutral" showLabel disabled={busy} onClick={() => decide('pass')}>
            <Icon icon={X} size="nav" />
          </IconButton>
          <IconButton label="Try it" size={62} tone="teal" showLabel disabled={busy} onClick={() => decide('try')}>
            <Icon icon={Heart} size="nav" />
          </IconButton>
        </div>
        {decision.isError && (
          <ErrorBanner onRetry={() => submit(decision.variables!)}>
            Couldn't save that choice. {decision.error.message}
          </ErrorBanner>
        )}
      </>
    )
  }

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <p className={styles.kicker}>Discover</p>
          <h1 className={styles.title}>Side quests</h1>
        </div>
        {questLog.isSuccess && <QuestLogPill dinners={questLog.data} />}
      </header>
      {questLog.isError && (
        <ErrorBanner onRetry={() => void questLog.refetch()}>Couldn't load the Quest log. {questLog.error.message}</ErrorBanner>
      )}
      {body}
    </div>
  )
}
