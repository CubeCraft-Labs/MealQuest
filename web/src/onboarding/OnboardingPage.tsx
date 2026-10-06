import { Repeat, ScrollText, Sparkles, type LucideIcon } from 'lucide-react'
import { Route, Routes, useNavigate } from 'react-router'
import { Button } from '../ui/Button.tsx'
import { Icon } from '../ui/Icon.tsx'
import { NotificationsStep } from './NotificationsStep.tsx'
import { OnboardingStep } from './OnboardingStep.tsx'
import { RegularsStep } from './RegularsStep.tsx'
import styles from './Onboarding.module.css'

// OnboardingPage is /onboarding and its steps: the welcome, the Regulars
// (/onboarding/regulars) and notifications (/onboarding/notifications).
export function OnboardingPage() {
  return (
    <Routes>
      <Route index element={<WelcomeStep />} />
      <Route path="regulars" element={<RegularsStep />} />
      <Route path="notifications" element={<NotificationsStep />} />
    </Routes>
  )
}

const terms: { icon: LucideIcon; name: string; meaning: string }[] = [
  {
    icon: Repeat,
    name: 'Regulars',
    meaning: 'The dinners our kitchen makes on repeat. You add a few next.',
  },
  {
    icon: Sparkles,
    name: 'Side quests',
    meaning: 'New dinners MealQuest suggests from your Regulars. Try it or Pass.',
  },
  {
    icon: ScrollText,
    name: 'Quest log',
    meaning: 'The Side quests you said Try it to, waiting for a night on the plan.',
  },
]

function WelcomeStep() {
  const navigate = useNavigate()
  return (
    <OnboardingStep
      step={1}
      title="Welcome to MealQuest"
      action={
        <Button className={styles.action} onClick={() => void navigate('/onboarding/regulars')}>
          Let&apos;s start
        </Button>
      }
    >
      <p className={styles.helper}>MealQuest plans the household&apos;s dinners around what you already love to cook.</p>
      <ul className={styles.terms}>
        {terms.map(({ icon, name, meaning }) => (
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
    </OnboardingStep>
  )
}
