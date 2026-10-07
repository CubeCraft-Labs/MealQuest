import { useQueryClient } from '@tanstack/react-query'
import { Heart, X } from 'lucide-react'
import { useCallback, useEffect } from 'react'
import { useJob } from '../api/index.ts'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import {
  DECK_KEY,
  useDeck,
  useDecide,
  useQuestLogCount,
  useStartBatch,
  type BatchResult,
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

// DiscoverPage is mock 1a: the deck of Side quests to Try it or Pass, one
// at a time from the top card. Every decision — the buttons, the ← / →
// keys — goes through decide, which does nothing while busy.
export function DiscoverPage() {
  const queryClient = useQueryClient()
  const deck = useDeck()
  const questLog = useQuestLogCount()
  const decision = useDecide()
  const startBatch = useStartBatch()
  const job = useJob<BatchResult>(startBatch.data?.job_id ?? null)

  const top = deck.data?.[0]
  const busy = decision.isPending
  const jobRunning = startBatch.isSuccess && job.status !== 'succeeded' && job.status !== 'failed'
  const finding = startBatch.isPending || jobRunning

  const { mutate: mutateDecision } = decision
  const decide = useCallback(
    (choice: Choice) => {
      if (busy || top === undefined) return
      mutateDecision({ id: top.id, choice })
    },
    [busy, top, mutateDecision],
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
  const stored = job.result?.suggestion_ids.length ?? 0
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
      </section>
    )
  } else if (top === undefined) {
    body = (
      <>
        {startBatch.isError && (
          <ErrorBanner onRetry={() => startBatch.mutate()}>Couldn't ask for more side quests. {startBatch.error.message}</ErrorBanner>
        )}
        {job.status === 'failed' && job.error !== null && (
          <ErrorBanner onRetry={() => startBatch.mutate()}>Couldn't find new side quests. {job.error.message}</ErrorBanner>
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
          <button type="button" className={styles.action} disabled={busy} onClick={() => decide('pass')}>
            <span className={`${styles.circle} ${styles.pass}`} aria-hidden="true">
              <Icon icon={X} size="nav" />
            </span>
            Pass
          </button>
          <button type="button" className={styles.action} disabled={busy} onClick={() => decide('try')}>
            <span className={`${styles.circle} ${styles.tryIt}`} aria-hidden="true">
              <Icon icon={Heart} size="nav" />
            </span>
            Try it
          </button>
        </div>
        {decision.isError && (
          <ErrorBanner onRetry={() => mutateDecision(decision.variables!)}>
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
        {questLog.isSuccess && <span className={styles.questLog}>Quest log · {questLog.data}</span>}
      </header>
      {questLog.isError && (
        <ErrorBanner onRetry={() => void questLog.refetch()}>Couldn't load the Quest log. {questLog.error.message}</ErrorBanner>
      )}
      {body}
    </div>
  )
}
