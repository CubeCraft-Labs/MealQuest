import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CalendarDays, ShoppingBasket, Sparkles, UtensilsCrossed, type LucideIcon } from 'lucide-react'
import { useNavigate } from 'react-router'
import { InstallHint, isIos, isStandalone } from '../pwa/InstallHint.tsx'
import type { SubscribePushResult } from '../push/subscribe.ts'
import { usePush } from '../push/usePush.ts'
import { PUSH_STATUS_KEY } from '../settings/notificationsApi.ts'
import { needsMoreRegulars, useCompleteOnboarding } from '../settings/api.ts'
import { Button } from '../ui/Button.tsx'
import { Icon } from '../ui/Icon.tsx'
import { OnboardingStep } from './OnboardingStep.tsx'
import styles from './Onboarding.module.css'

// IOS_UNSUPPORTED says how to get notifications on iPhone and iPad, where
// Web Push reaches only the app installed to the Home Screen, whichever
// browser the page is open in.
const IOS_UNSUPPORTED =
  "Notifications aren't on: on iPhone and iPad they only work once MealQuest is on the Home Screen. Tap Share, then Add to Home Screen, open MealQuest from there, and turn them on in Settings under Notifications."

// resultText says how turning on notifications ended. 'dismissed' leaves the
// choice open, so it is the only result that keeps "Turn on notifications".
function resultText(result: SubscribePushResult): string {
  switch (result) {
    case 'subscribed':
      return "Notifications are on for this device. MealQuest will let the household know when there's something to look at."
    case 'denied':
      return "Notifications aren't on: this browser blocked them. To turn them on later, allow notifications for this site in the browser's site settings, then turn them on in Settings under Notifications."
    case 'unsupported':
      return isIos(navigator) && !isStandalone()
        ? IOS_UNSUPPORTED
        : "Notifications aren't on: this browser can't receive them, so nothing was turned on."
    case 'dismissed':
      return "The permission prompt was closed without an answer, so notifications are still off. Turn them on to be asked again, or choose Maybe later."
  }
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

// NotificationsStep is onboarding step 3: what MealQuest notifies about, the
// iOS install hint, and finishing onboarding. "Turn on notifications" turns
// on Web Push on this device from that tap — the only place onboarding asks
// for permission — and says how it ended: on, blocked with how to allow it
// later, or not supported with how to get it on iPhone. Then only "Finish
// setting up" is offered, except after a dismissed prompt, which can be
// asked again. "Maybe later" finishes without asking. When the server
// refuses to finish because the household has fewer than three Regulars,
// the reason shows with a way back to step 2 to add them.
export function NotificationsStep() {
  const navigate = useNavigate()
  const push = usePush()
  const queryClient = useQueryClient()
  const complete = useCompleteOnboarding()
  const [enabling, setEnabling] = useState(false)
  const [problem, setProblem] = useState<string | null>(null)
  const [fewRegulars, setFewRegulars] = useState(false)
  const [result, setResult] = useState<SubscribePushResult | null>(null)
  const busy = enabling || complete.isPending
  const decided = result !== null && result !== 'dismissed'

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

  // turnOn calls subscribePush straight from the tap, before anything is
  // awaited, so the browser lets it ask for permission.
  async function turnOn() {
    clearProblem()
    setResult(null)
    setEnabling(true)
    try {
      setResult(await push.subscribePush())
    } catch (error) {
      setProblem(`Could not turn on notifications: ${(error as Error).message}`)
    } finally {
      setEnabling(false)
      void queryClient.invalidateQueries({ queryKey: PUSH_STATUS_KEY })
    }
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
        decided ? (
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
      {result !== null && (
        <p role="status" className={styles.result}>
          {resultText(result)}
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
