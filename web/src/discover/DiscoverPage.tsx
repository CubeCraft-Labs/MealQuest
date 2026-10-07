import { useQueryClient } from '@tanstack/react-query'
import { Heart, X } from 'lucide-react'
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type PointerEvent, type Ref } from 'react'
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
  type SideQuest,
} from './api.ts'
import { DeckCard, DECK_DEPTH, type SwipeHandlers } from './DeckCard.tsx'
import {
  bump,
  fadeOut,
  follow,
  isAbort,
  pass,
  prefersReducedMotion,
  promote,
  snapBack,
  tryIt,
  unfollow,
  type Exit,
} from './motion.ts'
import styles from './DiscoverPage.module.css'

// SWIPE_THRESHOLD is how far, in px, the top card must be dragged left or
// right for its release to Pass or Try it.
const SWIPE_THRESHOLD = 110

// A Drag is the pointer dragging the top card: where it went down, and the
// card's drag transform so far.
type Drag = { pointerId: number; x: number; y: number; dx: number; transform: string | null }

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
// ref is the visible pill, which Try it bumps.
function QuestLogPill({ dinners, ref }: { dinners: { id: string; name: string }[]; ref: Ref<HTMLSpanElement> }) {
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
        <span ref={ref} className={styles.questLog}>Quest log · {dinners.length}</span>
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
// at a time from the top card. A decision from the buttons, the ← / → keys
// or a swipe of the top card throws it out (motion.ts) and sends the
// decision once it is gone, taking the card off the deck at once (see
// useDecide); Retry sends a failed one again while the deck it read after
// failing still had its Side quest pending. Input is locked from the press
// until the decision settles, which is once the deck and the Quest log have
// been read again after it, so no decision starts under those reads. The lock is a
// ref, set before anything starts, so a second press before the next render
// does nothing, and busy mirrors it to disable the buttons.
export function DiscoverPage() {
  const queryClient = useQueryClient()
  const deck = useDeck()
  const questLog = useQuestLog()
  const decision = useDecide()
  const startBatch = useStartBatch()
  // The job of the batch last known to be finding Side quests: the one Get
  // more now started or joined, or the replenish a decision started.
  const [batchJobId, setBatchJobId] = useState<string | null>(null)
  const job = useJob<unknown>(BATCH_JOB_KIND, batchJobId)

  const top = deck.data?.[0]
  const [busy, setBusy] = useState(false)
  const jobRunning = batchJobId !== null && job.status !== 'succeeded' && job.status !== 'failed'
  const finding = startBatch.isPending || jobRunning
  const { mutate: mutateStartBatch } = startBatch
  const getMore = useCallback(() => mutateStartBatch(undefined, { onSuccess: (b) => setBatchJobId(b.job_id) }), [mutateStartBatch])

  // The batch's result as far as it has gone, or why it cannot be read.
  const batch = useMemo(() => {
    if (job.result === null) return { stored: 0, error: null }
    try {
      return { stored: parseBatchResult(`GET /api/v1/jobs/${batchJobId}`, job.result).suggestion_ids.length, error: null }
    } catch (error) {
      return { stored: 0, error: error as Error }
    }
  }, [job.result, batchJobId])
  // The job's failure, the latest poll's failure, an answer that is not the
  // batch's job, or an unreadable result.
  const batchError = job.error ?? batch.error

  const deckRef = useRef<HTMLDivElement>(null)
  const passRef = useRef<HTMLButtonElement>(null)
  const tryRef = useRef<HTMLButtonElement>(null)
  const questLogRef = useRef<HTMLSpanElement>(null)
  // The top card's exit until its decision settles, and the Side quest the
  // last decision sent was for, until the deck moves past it.
  const exit = useRef<Exit | null>(null)
  const leaving = useRef<string | null>(null)
  const drag = useRef<Drag | null>(null)
  // The top card's snap-back while it may still run; a new drag or a
  // decision stops it so it does not fight the card's new motion.
  const springing = useRef<Animation | null>(null)
  const stopSpring = useCallback(() => {
    springing.current?.cancel()
    springing.current = null
  }, [])

  const { mutate: mutateDecision } = decision
  const deciding = useRef(false)
  const lock = useCallback(() => {
    if (deciding.current) return false
    deciding.current = true
    setBusy(true)
    return true
  }, [])
  // send records a decision. A failed one stops the card's exit, so a card
  // useDecide put back on the deck is at rest; a saved one that started a
  // replenish batch follows its job.
  const send = useCallback(
    (variables: { quest: SideQuest; choice: Choice }) => {
      leaving.current = variables.quest.id
      mutateDecision(variables, {
        onSuccess: (replenishJobId) => {
          if (replenishJobId !== null) setBatchJobId(replenishJobId)
        },
        onError: () => {
          exit.current?.cancel()
          leaving.current = null
        },
        onSettled: () => {
          exit.current = null
          deciding.current = false
          setBusy(false)
        },
      })
    },
    [mutateDecision],
  )
  const submit = useCallback(
    (variables: { quest: SideQuest; choice: Choice }) => {
      if (lock()) send(variables)
    },
    [lock, send],
  )
  // decide bumps the button, throws the top card out — or, motion reduced,
  // fades it — then sends the decision; Try it bumps the Quest log pill too.
  // A swipe's decision comes from the card's drag transform: it is thrown
  // from there without a bump or wiggle. A button or key decision ends a
  // drag in progress, putting the card back first.
  const decide = useCallback(
    (choice: Choice, from?: string) => {
      if (top === undefined || !lock()) return
      stopSpring()
      const card = deckRef.current!.querySelector('article')!
      if (from === undefined && drag.current !== null) {
        drag.current = null
        unfollow(card)
      }
      const reduced = prefersReducedMotion()
      let run: Exit
      if (reduced) {
        run = fadeOut(card)
      } else if (from === undefined) {
        bump((choice === 'try' ? tryRef : passRef).current!)
        run = choice === 'try' ? tryIt(card) : pass(card)
      } else {
        run = choice === 'try' ? tryIt(card, from) : pass(card, from)
      }
      if (from !== undefined) {
        // Putting a swiped card back puts it back at rest.
        const thrown = run
        run = {
          finished: thrown.finished,
          cancel: () => {
            thrown.cancel()
            unfollow(card)
          },
        }
      }
      exit.current = run
      run.finished.then(
        () => {
          // The Quest log pill is missing while the Quest log fails to load.
          if (choice === 'try' && !reduced && questLogRef.current !== null) bump(questLogRef.current)
          send({ quest: top, choice })
        },
        (error: unknown) => {
          if (!isAbort(error)) throw error
        },
      )
    },
    [top, lock, send, stopSpring],
  )

  // swipe drags the top card: it follows the pointer, captured from the
  // press, until the release decides past the threshold or springs it back
  // short of it. The release decides from where the pointer let go, which
  // may differ from its last move. A cancel, or losing the capture before
  // the release, springs it back from its last move. One pointer drags at
  // a time: a press while a drag is in progress is ignored, as is input
  // while busy, and a drag held into a busy spell (a Retry) is put back on
  // release.
  const swipe = useMemo<SwipeHandlers>(() => {
    function track(card: HTMLElement, current: Drag, event: PointerEvent<HTMLElement>) {
      current.dx = event.clientX - current.x
      current.transform = follow(card, current.dx, event.clientY - current.y)
    }
    function release(event: PointerEvent<HTMLElement>, decides: boolean) {
      const card = event.currentTarget
      const current = drag.current
      if (current === null || current.pointerId !== event.pointerId) return
      drag.current = null
      const moved = current.transform !== null || event.clientX !== current.x || event.clientY !== current.y
      if (decides && moved && !deciding.current) track(card, current, event)
      if (current.transform === null) return
      if (deciding.current) unfollow(card)
      else if (decides && current.dx > SWIPE_THRESHOLD) decide('try', current.transform)
      else if (decides && current.dx < -SWIPE_THRESHOLD) decide('pass', current.transform)
      else if (prefersReducedMotion()) unfollow(card)
      else springing.current = snapBack(card, current.transform)
    }
    return {
      onPointerDown: (event) => {
        if (drag.current !== null || deciding.current || !event.isPrimary || event.button !== 0) return
        stopSpring()
        event.currentTarget.setPointerCapture(event.pointerId)
        drag.current = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, dx: 0, transform: null }
      },
      onPointerMove: (event) => {
        const current = drag.current
        if (current === null || current.pointerId !== event.pointerId || deciding.current) return
        track(event.currentTarget, current, event)
      },
      onPointerUp: (event) => release(event, true),
      onPointerCancel: (event) => release(event, false),
      // After a release the drag is already over, so this only ends a drag
      // whose capture was lost before it.
      onLostPointerCapture: (event) => release(event, false),
    }
  }, [decide, stopSpring])

  // Leaving the page stops an exit before its decision is sent.
  useEffect(() => () => exit.current?.cancel(), [])

  const topId = top?.id
  // A drag belongs to the card it started on, which a new top card ends.
  useLayoutEffect(() => {
    drag.current = null
  }, [topId])

  // Once the deck moves past the card a decision was sent for, the cards
  // behind it move up a depth.
  useLayoutEffect(() => {
    if (leaving.current === null || leaving.current === topId) return
    leaving.current = null
    if (topId === undefined || prefersReducedMotion()) return
    promote([...deckRef.current!.querySelectorAll<HTMLElement>(':scope > article')])
  }, [topId])

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
  // whenever it reports more, and once it ends. A read already out may be a
  // decision's, which must not be cut short: the batch's read waits for it,
  // then goes out after it, as it may predate what the batch reported.
  const { stored } = batch
  useEffect(() => {
    if (job.status === undefined) return
    const after = queryClient.isFetching({ queryKey: DECK_KEY }) > 0
    void queryClient
      .invalidateQueries({ queryKey: DECK_KEY }, { cancelRefetch: false })
      .then(() => (after ? queryClient.invalidateQueries({ queryKey: DECK_KEY }, { cancelRefetch: false }) : undefined))
  }, [queryClient, job.status, stored])

  // A failed decision can be retried only while the deck it read after
  // failing still has its Side quest pending: anything else decided it
  // already.
  const pending = decision.isError ? decision.error.pending : undefined
  const retryable = pending !== undefined && pending !== null

  let body
  if (deck.isError) {
    body = <ErrorBanner onRetry={() => void deck.refetch()}>Couldn't load your side quests. {deck.error.message}</ErrorBanner>
  } else if (deck.isPending || (deck.data.length === 0 && (finding || decision.isPending))) {
    // An empty deck is not caught up while a batch finds more Side quests,
    // or while the last decision may yet start one or come back refused.
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
          <ErrorBanner onRetry={getMore}>Couldn't ask for more side quests. {startBatch.error.message}</ErrorBanner>
        )}
        {batchError !== null && (
          <ErrorBanner onRetry={getMore}>Couldn't find new side quests. {batchError.message}</ErrorBanner>
        )}
        <EmptyState title="All caught up" action={{ label: 'Get more now', onClick: getMore }}>
          New side quests arrive Sunday.
        </EmptyState>
      </>
    )
  } else {
    body = (
      <>
        <div ref={deckRef} className={styles.deck}>
          {deck.data.slice(0, DECK_DEPTH.length).map((quest, depth) => (
            <DeckCard key={quest.id} quest={quest} depth={depth} swipe={depth === 0 ? swipe : undefined} />
          ))}
        </div>
        <div className={styles.actions}>
          <IconButton ref={passRef} label="Pass" size={62} tone="neutral" showLabel disabled={busy} onClick={() => decide('pass')}>
            <Icon icon={X} size="nav" />
          </IconButton>
          <IconButton ref={tryRef} label="Try it" size={62} tone="teal" showLabel disabled={busy} onClick={() => decide('try')}>
            <Icon icon={Heart} size="nav" />
          </IconButton>
        </div>
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
        {questLog.isSuccess && <QuestLogPill ref={questLogRef} dinners={questLog.data} />}
      </header>
      {questLog.isError && (
        <ErrorBanner onRetry={() => void questLog.refetch()}>Couldn't load the Quest log. {questLog.error.message}</ErrorBanner>
      )}
      {body}
      {/* A failed decision is shown whatever the deck has come to since. */}
      {decision.isError &&
        (retryable ? (
          <ErrorBanner onRetry={() => submit(decision.variables)}>Couldn't save that choice. {decision.error.message}</ErrorBanner>
        ) : pending === undefined ? (
          <ErrorBanner>Couldn't save that choice. {decision.error.message}</ErrorBanner>
        ) : (
          <ErrorBanner>That side quest has been decided already. {decision.error.message}</ErrorBanner>
        ))}
    </div>
  )
}
