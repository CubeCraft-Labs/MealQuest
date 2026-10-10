import { Button } from '../ui/Button.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Photo } from '../ui/Photo.tsx'
import { useDecideRotationReview, useRotationReviews, type Decision, type RotationReview } from './rotation.ts'
import styles from './RotationCheck.module.css'

// CONFIRMATION is what Home says once the household has answered.
const CONFIRMATION: Record<Decision, string> = {
  keep: 'Added to your Regulars',
  drop: "Got it — we won't suggest it again",
  not_made: 'Back in your Quest log',
}

type RotationCardProps = { review: RotationReview; disabled: boolean; onDecide: (decision: Decision) => void }

// RotationCard asks "Keep it in rotation?" about a tried dinner: its washed
// photo, "Last night: {dinner}", and the three answers.
function RotationCard({ review, disabled, onDecide }: RotationCardProps) {
  const { dinner } = review
  return (
    <section aria-label="Keep it in rotation?" className={styles.card}>
      <Photo id={dinner.photo_id} name={dinner.name} inset="10px" sizes="400px" className={styles.photo} />
      <div className={styles.body}>
        <p className={styles.kicker}>Last night: {dinner.name}</p>
        <h2 className={styles.question}>Keep it in rotation?</h2>
        <div className={styles.actions}>
          <Button disabled={disabled} onClick={() => onDecide('keep')}>
            Keep it
          </Button>
          <Button variant="outline" disabled={disabled} onClick={() => onDecide('drop')}>
            Not for us
          </Button>
        </div>
        <button type="button" className={styles.notMade} disabled={disabled} onClick={() => onDecide('not_made')}>
          We didn't make it
        </button>
      </div>
    </section>
  )
}

// RotationCheck is the Rotation check on Home: the card for the oldest
// pending check (GET /api/v1/rotation-reviews), which stays until the
// household answers it — nothing is decided for them. An answer is applied
// at once: Home confirms it and shows the next pending check, if any, while
// the answer is saved; the answers are locked until it settles. A failed
// answer puts its check back with an ErrorBanner. A failed read shows an
// ErrorBanner, beside the checks last read: a card already shown stays
// until it is answered.
export function RotationCheck() {
  const reviews = useRotationReviews()
  const decide = useDecideRotationReview()
  const deciding = decide.isPending ? decide.variables.review.dinner.id : null
  const next = reviews.data?.find((review) => review.dinner.id !== deciding)
  const answered = decide.isPending || decide.isSuccess ? decide.variables : undefined
  return (
    <>
      {answered !== undefined && (
        <p role="status" className={styles.confirmation}>
          {CONFIRMATION[answered.decision]}
        </p>
      )}
      {reviews.isError && (
        <ErrorBanner onRetry={() => void reviews.refetch()}>
          Couldn't load the Rotation check. {reviews.error.message}
        </ErrorBanner>
      )}
      {decide.isError && <ErrorBanner>Couldn't save that answer. {decide.error.message}</ErrorBanner>}
      {next !== undefined && (
        <RotationCard
          key={next.dinner.id}
          review={next}
          disabled={decide.isPending}
          onDecide={(decision) => decide.mutate({ review: next, decision })}
        />
      )}
    </>
  )
}
