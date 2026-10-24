import { useState, type FormEvent } from 'react'
import { Plus } from 'lucide-react'
import { useNavigate } from 'react-router'
import { useAddRegular, useRegulars, type Regular } from '../dinners/api.ts'
import { similarNames } from '../dinners/names.ts'
import { useRemoveRegular } from '../dinners/removeRegular.ts'
import { useRefusals } from '../dinners/useRefusals.ts'
import { Button } from '../ui/Button.tsx'
import { Chip } from '../ui/Chip.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { TextInput } from '../ui/TextInput.tsx'
import { OnboardingStep } from './OnboardingStep.tsx'
import styles from './Onboarding.module.css'

// MIN_REGULARS is how many Regulars the server has saved before the
// household moves on.
export const MIN_REGULARS = 3

// commonPicks are offered as one-tap Regulars until the household has them,
// or a Regular the server would count as the same dinner.
export const commonPicks = [
  'Beef tacos',
  'Spaghetti bolognese',
  'Chicken stir-fry',
  'Pizza night',
  'Chili',
  'Pot roast',
  'Burgers',
  'Fried rice',
]

// RegularsStep is onboarding step 2, handoff mock 1h: the household adds
// the dinners it makes on repeat. Each add and remove shows at once and is
// undone if the server refuses it.
export function RegularsStep() {
  const navigate = useNavigate()
  const regulars = useRegulars()
  const add = useAddRegular()
  const remove = useRemoveRegular()
  const [draft, setDraft] = useState('')
  const { refusals, track } = useRefusals()
  const list = regulars.data ?? []
  const picks = commonPicks.filter((pick) => !list.some((regular) => similarNames(regular.name, pick)))
  // Next waits for every add to be answered, so a refused add's error shows
  // here and an add the server takes back never counts towards the three.
  const adding = list.some((regular) => regular.pending)
  const saved = list.filter((regular) => !regular.pending).length

  function addRegular(name: string) {
    track(`add:${name.toLowerCase()}`, `add ${name}`, add.mutateAsync({ name }))
  }

  function removeRegular(regular: Regular) {
    track(`remove:${regular.id}`, `remove ${regular.name}`, remove.mutateAsync(regular))
  }

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = draft.trim()
    if (name === '') return
    addRegular(name)
    setDraft('')
  }

  return (
    <OnboardingStep
      step={2}
      title="What's on your usual rotation?"
      action={
        <Button
          className={styles.action}
          disabled={adding || saved < MIN_REGULARS}
          onClick={() => void navigate('/onboarding/notifications')}
        >
          Next: notifications
        </Button>
      }
    >
      <p className={styles.helper}>
        Add the dinners your household makes on repeat. MealQuest builds suggestions from these.
      </p>
      <form className={styles.add} onSubmit={onSubmit}>
        <TextInput
          label="Dinner name"
          labelHidden
          className={styles.addInput}
          placeholder="e.g. Chicken enchiladas"
          autoComplete="off"
          enterKeyHint="done"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
        <button type="submit" className={styles.addButton} aria-label="Add Regular" disabled={draft.trim() === ''}>
          <span className={styles.addFace}>
            <Icon icon={Plus} size="nav" />
          </span>
        </button>
      </form>
      {refusals.map((refusal) => (
        <p key={refusal.key} role="alert" className={styles.problem}>
          {refusal.text}
        </p>
      ))}

      <h2 className={styles.groupLabel}>Your regulars · {list.length}</h2>
      {regulars.isPending && <Skeleton className={styles.status} />}
      {regulars.isError && (
        <div className={styles.status}>
          <ErrorBanner onRetry={() => void regulars.refetch()}>
            Your Regulars could not load: {regulars.error.message}
          </ErrorBanner>
        </div>
      )}
      {list.length > 0 && (
        <ul className={styles.chips}>
          {list.map((regular: Regular) => (
            <li key={regular.id}>
              <Chip
                label={regular.name}
                className={regular.pending && styles.pending}
                aria-busy={regular.pending}
                onRemove={regular.pending ? undefined : () => removeRegular(regular)}
              />
            </li>
          ))}
        </ul>
      )}

      {regulars.isSuccess && picks.length > 0 && (
        <>
          <h2 className={styles.groupLabel}>Common picks</h2>
          <ul className={styles.chips}>
            {picks.map((pick) => (
              <li key={pick}>
                <button type="button" className={styles.pick} aria-label={`Add ${pick}`} onClick={() => addRegular(pick)}>
                  <span aria-hidden="true">+</span>
                  {pick}
                </button>
              </li>
            ))}
          </ul>
        </>
      )}
    </OnboardingStep>
  )
}
