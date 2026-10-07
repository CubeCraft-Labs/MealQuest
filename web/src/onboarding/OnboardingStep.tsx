import type { ReactNode } from 'react'
import styles from './Onboarding.module.css'

export const ONBOARDING_STEPS = 3

export type OnboardingStepProps = {
  // 1-based; the progress pills fill up to it.
  step: number
  title: string
  children: ReactNode
  // The step's primary action, fixed to the bottom of the screen.
  action?: ReactNode
}

// OnboardingStep is the frame every onboarding step shares: progress pills,
// the "Step N of 3" kicker and the Bagel title.
export function OnboardingStep({ step, title, children, action }: OnboardingStepProps) {
  return (
    <main className={styles.screen}>
      <div className={styles.column}>
        <div className={styles.progress} aria-hidden="true">
          {Array.from({ length: ONBOARDING_STEPS }, (_, i) => (
            <span key={i} className={[styles.pill, i < step && styles.filled].filter(Boolean).join(' ')} />
          ))}
        </div>
        <p className={styles.kicker}>
          Step {step} of {ONBOARDING_STEPS}
        </p>
        <h1 className={styles.title}>{title}</h1>
        {children}
      </div>
      {action && <div className={styles.footer}>{action}</div>}
    </main>
  )
}
