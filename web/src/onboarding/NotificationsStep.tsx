import { useState } from 'react'
import { CalendarDays, ShoppingBasket, Sparkles, UtensilsCrossed, type LucideIcon } from 'lucide-react'
import { useNavigate } from 'react-router'
import { InstallHint } from '../pwa/InstallHint.tsx'
import { needsMoreRegulars, useCompleteOnboarding } from '../settings/api.ts'
import { Button } from '../ui/Button.tsx'
import { Icon } from '../ui/Icon.tsx'
import { OnboardingStep } from './OnboardingStep.tsx'
import styles from './Onboarding.module.css'

// NotificationsResult is how asking to turn on notifications ended.
export type NotificationsResult = 'enabled' | 'denied' | 'unavailable'

export type EnableNotifications = () => Promise<NotificationsResult>

// TODO(Feature 19): Web Push. Until it is wired, notifications cannot be
// turned on, and nothing asks the browser for permission. This placeholder
// is the interim state: "Turn on notifications" answers 'unavailable', and
// the step tells the household plainly that nothing was turned on.
export function notificationsUnavailable(): Promise<NotificationsResult> {
  return Promise.resolve('unavailable')
}

// notOnReasons says why notifications are not on after asking, for each
// result other than 'enabled'.
const notOnReasons: Record<Exclude<NotificationsResult, 'enabled'>, string> = {
  unavailable:
    "Notifications aren't on yet: MealQuest can't send them until the notification feature ships, so nothing was turned on. You can finish setting up now.",
  denied: "Notifications aren't on: this browser blocked them. You can allow them in the browser's settings later.",
}

const topics: { icon: LucideIcon; name: string; meaning: string }[] = [
  {
    icon: CalendarDays,
    name: 'Plan ready',
    meaning: "The week's Draft board is ready for the household to adjust.",
  },
  {
    icon: ShoppingBasket,
    name: 'Supply run ready',
    meaning: "The week's shopping list, grouped by dinner, is ready to shop.",
  },
  {
    icon: UtensilsCrossed,
    name: "Tonight's dinner",
    meaning: "A reminder before dinner of what's on the plan tonight.",
  },
  {
    icon: Sparkles,
    name: 'New Side quests',
    meaning: 'Fresh dinners to Try it or Pass, picked from your Regulars.',
  },
]

export type NotificationsStepProps = {
  // Asks to turn on notifications when the household taps "Turn on
  // notifications" — never on render.
  enableNotifications?: EnableNotifications
}

// NotificationsStep is onboarding step 3: what MealQuest notifies about, the
// iOS install hint, and finishing onboarding. "Maybe later", and "Turn on
// notifications" once they are enabled, record that onboarding is finished
// and go Home. When they are not enabled, the step says so and offers only
// "Finish setting up". When the server refuses because the household has
// fewer than three Regulars, the reason shows with a way back to step 2 to
// add them.
export function NotificationsStep({ enableNotifications = notificationsUnavailable }: NotificationsStepProps) {
  const navigate = useNavigate()
  const complete = useCompleteOnboarding()
  const [enabling, setEnabling] = useState(false)
  const [problem, setProblem] = useState<string | null>(null)
  const [fewRegulars, setFewRegulars] = useState(false)
  const [notOn, setNotOn] = useState<string | null>(null)
  const busy = enabling || complete.isPending

  async function finish() {
    try {
      await complete.mutateAsync()
    } catch (error) {
      setProblem(`Could not finish setting up: ${(error as Error).message}`)
      setFewRegulars(needsMoreRegulars(error as Error))
      return
    }
    void navigate('/', { replace: true })
  }

  function clearProblem() {
    setProblem(null)
    setFewRegulars(false)
  }

  async function turnOn() {
    clearProblem()
    setEnabling(true)
    let result: NotificationsResult
    try {
      result = await enableNotifications()
    } catch (error) {
      setProblem(`Could not turn on notifications: ${(error as Error).message}`)
      return
    } finally {
      setEnabling(false)
    }
    if (result !== 'enabled') {
      setNotOn(notOnReasons[result])
      return
    }
    await finish()
  }

  function later() {
    clearProblem()
    void finish()
  }

  return (
    <OnboardingStep
      step={3}
      title="Stay in the loop"
      action={
        notOn ? (
          <div className={styles.actions}>
            <Button className={styles.action} disabled={busy} onClick={later}>
              Finish setting up
            </Button>
          </div>
        ) : (
          <div className={styles.actions}>
            <Button className={styles.action} disabled={busy} onClick={() => void turnOn()}>
              Turn on notifications
            </Button>
            <Button variant="outline" className={styles.action} disabled={busy} onClick={later}>
              Maybe later
            </Button>
          </div>
        )
      }
    >
      <p className={styles.helper}>MealQuest can let the household know when there&apos;s something to look at:</p>
      <ul className={styles.terms}>
        {topics.map(({ icon, name, meaning }) => (
          <li key={name} className={styles.term}>
            <span className={styles.termIcon}>
              <Icon icon={icon} size="nav" />
            </span>
            <p className={styles.termText}>
              <span className={styles.termName}>{name}</span>
              {meaning}
            </p>
          </li>
        ))}
      </ul>
      <div className={styles.hint}>
        <InstallHint />
      </div>
      {notOn && (
        <p role="status" className={styles.notOn}>
          {notOn}
        </p>
      )}
      {problem && (
        <p role="alert" className={styles.problem}>
          {problem}
        </p>
      )}
      {fewRegulars && (
        <Button variant="outline" className={styles.recover} onClick={() => void navigate('/onboarding/regulars')}>
          Add Regulars
        </Button>
      )}
    </OnboardingStep>
  )
}
