import { useQueryClient } from '@tanstack/react-query'
import { Check, ChevronLeft, ChevronRight, Heart } from 'lucide-react'
import { useCallback, useEffect, useId, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { ApiError } from '../api/errors.ts'
import { planKey, usePlan, useWeekSwap } from '../plan/api.ts'
import { duration } from '../plan/PlanPage.tsx'
import { addDays, dayAndDate, localNow, weekdayName, weekStart } from '../plan/week.ts'
import { useHouseholdSettings } from '../settings/api.ts'
import { useBreakpoint } from '../shell/useBreakpoint.ts'
import { Button } from '../ui/Button.tsx'
import buttonStyles from '../ui/Button.module.css'
import { Card } from '../ui/Card.tsx'
import { Checkbox } from '../ui/Checkbox.tsx'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Segmented, type SegmentedOption } from '../ui/Segmented.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import { Toggle } from '../ui/Toggle.tsx'
import { AddToWeekSheet, weekNames, type Added } from './AddToWeekSheet.tsx'
import { useToggleFavorite, type Difficulty } from './api.ts'
import { useDinnerDetail, type DinnerDetail, type Ingredient, type RecipeSource } from './detail.ts'
import styles from './DinnerDetailPage.module.css'
import { useKeepScreenOn } from './keepScreenOn.ts'
import { textSizeFailureText, useTextSize, type TextSize } from './textSize.ts'

// REGULAR_DINNERS is Settings → Regular dinners, where a recipe is fixed.
export const REGULAR_DINNERS = '/settings/regulars'

const difficultyLabels: Record<Difficulty, string> = { easy: 'Easy', medium: 'Medium', hard: 'Hard' }

// BackButton goes back in the app's history, or, opened from a link with
// no history in the app, to Home.
function BackButton() {
  const navigate = useNavigate()
  const location = useLocation()
  // The first entry of a session has the key "default".
  const back = () => (location.key === 'default' ? void navigate('/', { replace: true }) : void navigate(-1))
  return (
    <IconButton label="Back" tone="neutral" className={styles.back} onClick={back}>
      <Icon icon={ChevronLeft} size="inline" />
    </IconButton>
  )
}

// quantity is an ingredient's amount as the right-hand column shows it,
// "1 lb", or empty when the recipe gives none.
function quantity(ingredient: Ingredient): string {
  return [ingredient.qty, ingredient.unit].filter(Boolean).join(' ')
}

// IngredientRow is one ingredient: its name with any note, or the line as
// the recipe gives it when there is no name, and its amount on the right.
function IngredientRow({ ingredient }: { ingredient: Ingredient }) {
  return (
    <li className={styles.ingredient}>
      <span>
        {ingredient.name ?? ingredient.raw_text}
        {ingredient.name !== null && ingredient.note !== null && <span className={styles.note}>, {ingredient.note}</span>}
      </span>
      <span className={styles.qty}>{quantity(ingredient)}</span>
    </li>
  )
}

// SourceCredit is "Recipe from {site}" for a recipe fetched from the web: a
// link opening the recipe's page in a new tab when the server sends a url
// safe to open, else plain text. The site is the name the page gave, else
// the url's host — already in the link, so naming it shows nothing more —
// else, with neither to show, just "the web": a fetched recipe always
// credits where it came from, without a name the server withheld.
function SourceCredit({ siteName, url }: RecipeSource) {
  const credit = `Recipe from ${siteName ?? (url === null ? 'the web' : new URL(url).host)}`
  return (
    <p className={styles.source}>
      {url === null ? (
        credit
      ) : (
        <a href={url} target="_blank" rel="noopener noreferrer">
          {credit}
        </a>
      )}
    </p>
  )
}

// hasRecipe says whether the dinner's recipe was found or typed in, rather
// than still being searched for or not found.
function hasRecipe(dinner: DinnerDetail): boolean {
  return dinner.grounding_status !== 'pending' && dinner.grounding_status !== 'failed'
}

// RecipeMissing is what stands in for a recipe still being searched for, or
// the link to fix one that could not be found.
function RecipeMissing({ dinner }: { dinner: DinnerDetail }) {
  if (dinner.grounding_status === 'failed') {
    return (
      <section className={styles.missing} aria-label="No recipe">
        <p>We couldn't find a recipe for this dinner, so there are no ingredients or steps yet.</p>
        <Link to={REGULAR_DINNERS} className={styles.fix}>
          Fix recipe
        </Link>
      </section>
    )
  }
  return <p className={styles.missing}>Still finding the recipe — its ingredients and steps will show here.</p>
}

// Recipe is the dinner's ingredients and steps, or what stands in for them.
function Recipe({ dinner }: { dinner: DinnerDetail }) {
  if (!hasRecipe(dinner)) return <RecipeMissing dinner={dinner} />
  return (
    <>
      {dinner.ingredients.length > 0 && (
        <section className={styles.section}>
          <h2 className={styles.sectionTitle}>Ingredients</h2>
          <ul className={styles.ingredients}>
            {dinner.ingredients.map((ingredient, i) => (
              <IngredientRow key={i} ingredient={ingredient} />
            ))}
          </ul>
        </section>
      )}
      {dinner.steps.length > 0 && (
        <section className={styles.section}>
          <h2 className={styles.sectionTitle}>Steps</h2>
          <ol className={styles.steps}>
            {dinner.steps.map((step, i) => (
              <li key={i} className={styles.step}>
                <span className={styles.number} aria-hidden="true">
                  {i + 1}
                </span>
                <span>{step}</span>
              </li>
            ))}
          </ol>
        </section>
      )}
      {dinner.source !== null && <SourceCredit siteName={dinner.source.siteName} url={dinner.source.url} />}
    </>
  )
}

// AddedStatus says what became of a dinner added to a day: being saved,
// added, or why it couldn't be, with Retry, which sends the same swap again
// as the Draft board would (useWeekSwap), and is not offered while that is
// refused. The week's plan stays read while it shows, so a retry finds the
// day to swap; once added, the plan is read again from the server.
function AddedStatus({ added }: { added: Added }) {
  const queryClient = useQueryClient()
  usePlan(added.week)
  const { swap, refused, swapIn } = useWeekSwap(added.week)
  const { date } = added.swap
  const done = swap.succeeded === added.swap
  const failed = swap.failed?.swap === added.swap ? swap.failed.error : null
  useEffect(() => {
    if (done) void queryClient.invalidateQueries({ queryKey: planKey(added.week) })
  }, [done, queryClient, added.week])

  if (failed !== null) {
    return (
      <div className={styles.status}>
        <ErrorBanner onRetry={refused ? undefined : () => swapIn(added.swap)}>
          Couldn't add it to {weekdayName(date)} {weekNames[added.which]}. {failed.message}
        </ErrorBanner>
      </div>
    )
  }
  return (
    <p role="status" className={[styles.status, styles.added].join(' ')}>
      {done ? `Added to ${weekdayName(date)} ${weekNames[added.which]}.` : `Adding to ${weekdayName(date)}…`}
    </p>
  )
}

// AddToWeek is Add to this week: it opens the sheet of the week's days, and
// says what became of the dinner added.
function AddToWeek({ dinner }: { dinner: DinnerDetail }) {
  const [open, setOpen] = useState(false)
  const [added, setAdded] = useState<Added | null>(null)
  function add(chosen: Added) {
    setOpen(false)
    setAdded(chosen)
  }
  return (
    <>
      {added !== null && <AddedStatus key={`${added.week} ${added.swap.date}`} added={added} />}
      <Button className={styles.add} onClick={() => setOpen(true)}>
        Add to this week
      </Button>
      {open && <AddToWeekSheet dinner={dinner} onAdd={add} onClose={() => setOpen(false)} />}
    </>
  )
}

// FavoriteHeart toggles whether the dinner is a favorite, at once, changing
// back with the error and Retry if the server refuses. One toggle is sent at
// a time.
function FavoriteHeart({ dinner }: { dinner: DinnerDetail }) {
  const favorite = useToggleFavorite()
  function toggle() {
    if (!favorite.isPending) favorite.mutate({ id: dinner.id, is_favorite: dinner.is_favorite })
  }
  return (
    <>
      {favorite.isError && (
        <div className={styles.status}>
          <ErrorBanner onRetry={toggle}>
            Couldn't {dinner.is_favorite ? 'unfavorite' : 'favorite'} {dinner.name}. {favorite.error.message}
          </ErrorBanner>
        </div>
      )}
      <IconButton label="Favorite" size={52} aria-pressed={dinner.is_favorite} onClick={toggle}>
        <Icon icon={Heart} size="nav" className={dinner.is_favorite ? styles.filled : undefined} />
      </IconButton>
    </>
  )
}

// ActionBar is the bar at the bottom of the screen, the page's last child:
// the page fills the scroll area so a short recipe still leaves it at the
// bottom, and it sticks there over a long one. It holds Add to this week
// and the favorite heart, with what became of either above them, and in
// Kitchen reading mode the step buttons after them.
function ActionBar({ dinner, children }: { dinner: DinnerDetail; children?: ReactNode }) {
  return (
    <div className={styles.bar}>
      <AddToWeek dinner={dinner} />
      <FavoriteHeart dinner={dinner} />
      {children}
    </div>
  )
}

// Detail is a dinner as mock 1k shows it: the photo full-bleed under a sheet
// with its tags, title, why it was picked and its recipe, over the action bar.
function Detail({ dinner }: { dinner: DinnerDetail }) {
  const tags = [
    dinner.difficulty !== null && difficultyLabels[dinner.difficulty],
    dinner.minutes_total !== null && duration(dinner.minutes_total),
  ].filter((tag) => tag !== false)
  return (
    <>
      <div className={styles.hero}>
        <Photo id={dinner.photo_id} name={dinner.name} radius="0" sizes="100vw" className={styles.photo} />
        <BackButton />
      </div>
      <article className={styles.sheet}>
        <div className={styles.tags}>
          <Tag tone="grape">{dinner.status === 'regular' ? 'Regular' : 'Side quest'}</Tag>
          {tags.map((tag) => (
            <Tag key={tag} tone="neutral">
              {tag}
            </Tag>
          ))}
        </div>
        <h1 className={styles.title}>{dinner.name}</h1>
        {dinner.why_picked !== null && (
          <p className={styles.why}>
            <b>Why it was picked:</b> {dinner.why_picked}
          </p>
        )}
        <Recipe dinner={dinner} />
      </article>
      <ActionBar dinner={dinner} />
    </>
  )
}

// statusName is the dinner as the household calls it: a Regular, or a Side
// quest for any dinner not yet one.
function statusName(dinner: DinnerDetail): string {
  return dinner.status === 'regular' ? 'Regular' : 'Side quest'
}

// PlannedDay is the kicker of a dinner on a plan, "Saturday, Oct 10 · Side
// quest", whenever this week's or next week's plan has it. Of the days that
// do, it is the first from today on in the household's timezone; when every
// one is already past, it is the latest of them. There is none while the
// plans are read, nor when neither plan has the dinner.
function PlannedDay({ dinner, timezone }: { dinner: DinnerDetail; timezone: string }) {
  const today = localNow(timezone, new Date()).date
  const thisWeek = weekStart(today)
  const plans = [usePlan(thisWeek), usePlan(addDays(thisWeek, 7))]
  const failed = plans.find((plan) => plan.isError)
  if (failed !== undefined) {
    return (
      <ErrorBanner onRetry={() => plans.forEach((plan) => plan.isError && void plan.refetch())}>
        Couldn't check the plan for this dinner's day. {failed.error.message}
      </ErrorBanner>
    )
  }
  const planned = plans.flatMap((plan) => plan.data?.days ?? []).filter((day) => day.dinner.id === dinner.id)
  const day = planned.find((day) => day.date >= today) ?? planned.at(-1)
  if (day === undefined) return null
  return (
    <p className={styles.kicker}>
      {dayAndDate(day.date)} · {statusName(dinner)}
    </p>
  )
}

// Kicker is PlannedDay, once the household's timezone is read.
function Kicker({ dinner }: { dinner: DinnerDetail }) {
  const settings = useHouseholdSettings()
  if (settings.isError) {
    return (
      <ErrorBanner onRetry={() => void settings.refetch()}>
        Couldn't load the household settings to find this dinner's day. {settings.error.message}
      </ErrorBanner>
    )
  }
  if (settings.isPending) return null
  return <PlannedDay dinner={dinner} timezone={settings.data.timezone} />
}

const textSizes: SegmentedOption<TextSize>[] = [
  { value: 'small', label: 'A', ariaLabel: 'Small text' },
  { value: 'medium', label: 'A', ariaLabel: 'Medium text' },
  { value: 'large', label: 'A', ariaLabel: 'Large text' },
]

// ReadingHeader is Kitchen reading mode's header: back, the day the dinner
// is planned for, its title, Keep screen on and the text size.
function ReadingHeader({ dinner, textSize }: { dinner: DinnerDetail; textSize: ReturnType<typeof useTextSize> }) {
  const keepOnId = useId()
  const keepOnHintId = useId()
  const keepScreenOn = useKeepScreenOn()
  const { failure } = textSize
  return (
    <>
      <header className={styles.readingHeader}>
        <BackButton />
        <div className={styles.heading}>
          <Kicker dinner={dinner} />
          <h1 className={styles.readingTitle}>{dinner.name}</h1>
        </div>
        <div className={styles.keepOn}>
          <span id={keepOnId}>Keep screen on</span>
          <Toggle
            checked={keepScreenOn.on}
            onCheckedChange={keepScreenOn.setOn}
            aria-labelledby={keepOnId}
            disabled={!keepScreenOn.supported}
            aria-describedby={keepScreenOn.supported ? undefined : keepOnHintId}
          />
        </div>
        <Segmented
          label="Text size"
          options={textSizes}
          value={textSize.size}
          onValueChange={textSize.choose}
          className={styles.textSize}
        />
      </header>
      {!keepScreenOn.supported && (
        <p id={keepOnHintId} className={styles.keepOnHint}>
          This browser can't keep the screen on, so it may dim while you cook.
        </p>
      )}
      {keepScreenOn.failure !== null && (
        <ErrorBanner onRetry={() => keepScreenOn.setOn(true)}>
          Couldn't keep the screen on: {keepScreenOn.failure}
        </ErrorBanner>
      )}
      {failure !== null && (
        <ErrorBanner onRetry={textSize.retry}>
          {textSizeFailureText[failure.action]}: {failure.message}
        </ErrorBanner>
      )}
    </>
  )
}

// IngredientsCard is the ingredients with a checkbox each, struck through
// once set out, and how many are. Which are set out is this visit's alone:
// it is never sent to the server and is not the Supply run's "have it".
function IngredientsCard({ ingredients }: { ingredients: Ingredient[] }) {
  const titleId = useId()
  const [setOut, setSetOut] = useState<ReadonlySet<number>>(() => new Set())
  const count = ingredients.filter((_, i) => setOut.has(i)).length
  function toggle(i: number) {
    setSetOut((before) => {
      const after = new Set(before)
      if (!after.delete(i)) after.add(i)
      return after
    })
  }
  return (
    <Card role="region" aria-labelledby={titleId} className={styles.ingredientsCard}>
      <div className={styles.cardHead}>
        <h2 id={titleId} className={styles.readingSectionTitle}>
          Ingredients
        </h2>
        <span className={styles.setOutCount}>
          {count} of {ingredients.length} set out
        </span>
      </div>
      <ul className={styles.checklist}>
        {ingredients.map((ingredient, i) => (
          <li key={i} className={styles.checkRow}>
            <Checkbox
              className={styles.check}
              checked={setOut.has(i)}
              onChange={() => toggle(i)}
              label={
                <span className={[styles.checkName, setOut.has(i) && styles.setOut].filter(Boolean).join(' ')}>
                  {ingredient.name ?? ingredient.raw_text}
                  {ingredient.name !== null && ingredient.note !== null && `, ${ingredient.note}`}
                </span>
              }
            />
            <span className={styles.checkQty}>{quantity(ingredient)}</span>
          </li>
        ))}
      </ul>
    </Card>
  )
}

// Steps is the recipe's steps: those before current done, muted with a
// check; current highlighted in larger text; the rest still to come.
function Steps({ steps, current }: { steps: string[]; current: number }) {
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className={styles.readingSteps}>
      <h2 id={titleId} className={styles.readingSectionTitle}>
        Steps
      </h2>
      <ol className={styles.stepList}>
        {steps.map((step, i) => (
          <li
            key={i}
            aria-current={i === current ? 'step' : undefined}
            className={[styles.readingStep, i < current ? styles.stepDone : i === current ? styles.stepCurrent : undefined]
              .filter(Boolean)
              .join(' ')}
          >
            <span className={styles.stepCircle} aria-hidden="true">
              {i < current ? <Icon icon={Check} /> : i + 1}
            </span>
            <span className={styles.stepText}>{step}</span>
          </li>
        ))}
      </ol>
    </section>
  )
}

// typing says whether target is a field taking text, where the arrow keys
// move the caret and are not the screen's to take.
function typing(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  return target.isContentEditable || target.matches('textarea, select, input:not([type="checkbox"], [type="radio"])')
}

// useCurrentStep is the step being cooked, of count, from the first, with
// move, which goes back or on by a step and stops at either end. The ← and →
// keys move it too, unless a text field has focus, a sheet is open over the
// screen or the key was already taken (a segmented control's arrows).
function useCurrentStep(count: number): [number, (by: number) => void] {
  const [step, setStep] = useState(0)
  const last = count - 1
  const move = useCallback(
    (by: number) => setStep((before) => Math.max(0, Math.min(Math.min(before, last) + by, last))),
    [last],
  )
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      const by = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0
      if (by === 0 || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
      if (typing(event.target) || (event.target instanceof Element && event.target.closest('[role="dialog"]') !== null)) return
      event.preventDefault()
      move(by)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [move])
  return [Math.max(0, Math.min(step, last)), move]
}

// KitchenReading is a dinner as mock 2a shows it on a tablet: the header,
// then the ingredients to check off beside the photo, why it was picked and
// the steps, one at a time, over the action bar with Previous and Next step.
// What is checked and the step reached last only as long as this visit.
function KitchenReading({ dinner }: { dinner: DinnerDetail }) {
  const textSize = useTextSize()
  const [current, move] = useCurrentStep(dinner.steps.length)
  const recipe = hasRecipe(dinner)
  const last = dinner.steps.length - 1
  return (
    <div className={[styles.reading, styles[textSize.size]].join(' ')}>
      <ReadingHeader dinner={dinner} textSize={textSize} />
      <div className={recipe && dinner.ingredients.length > 0 ? styles.columns : styles.oneColumn}>
        {recipe && dinner.ingredients.length > 0 && <IngredientsCard ingredients={dinner.ingredients} />}
        <div className={styles.method}>
          <div className={styles.intro}>
            <Photo id={dinner.photo_id} name={dinner.name} radius="24px" sizes="400px" className={styles.introPhoto} />
            {dinner.why_picked !== null && (
              <p className={[styles.why, styles.introWhy].join(' ')}>
                <b>Why it was picked:</b> {dinner.why_picked}
              </p>
            )}
          </div>
          {!recipe && <RecipeMissing dinner={dinner} />}
          {recipe && dinner.steps.length > 0 && <Steps steps={dinner.steps} current={current} />}
          {recipe && dinner.source !== null && (
            <SourceCredit siteName={dinner.source.siteName} url={dinner.source.url} />
          )}
          <ActionBar dinner={dinner}>
            {recipe && dinner.steps.length > 0 && (
              <div className={styles.stepButtons}>
                <Button variant="outline" disabled={current === 0} onClick={() => move(-1)}>
                  <Icon icon={ChevronLeft} />
                  Previous
                </Button>
                <Button disabled={current === last} onClick={() => move(1)}>
                  <Icon icon={ChevronRight} />
                  Next step
                </Button>
              </div>
            )}
          </ActionBar>
        </div>
      </div>
    </div>
  )
}

// DinnerDetailPage is a dinner with its recipe: on a phone (mock 1k) its
// photo, or the striped placeholder, under a sheet with Regular or Side
// quest, difficulty and time, why it was picked, the ingredients and the
// steps; from tablet up, Kitchen reading mode (mock 2a). A dinner whose
// recipe could not be found links to Settings → Regular dinners to fix it;
// an unknown or removed dinner says it is not found.
export function DinnerDetailPage() {
  const { id = '' } = useParams()
  const layout = useBreakpoint()
  const dinner = useDinnerDetail(id)

  let body
  if (dinner.isError) {
    body = (
      <>
        <div className={styles.backRow}>
          <BackButton />
        </div>
        {dinner.error instanceof ApiError && dinner.error.status === 404 ? (
          <>
            <EmptyState title="Dinner not found">This dinner isn't in our kitchen — it may have been removed.</EmptyState>
            <Link to="/" className={[buttonStyles.button, buttonStyles.outline].join(' ')}>
              Go Home
            </Link>
          </>
        ) : (
          <ErrorBanner onRetry={() => void dinner.refetch()}>Couldn't load this dinner. {dinner.error.message}</ErrorBanner>
        )}
      </>
    )
  } else if (dinner.isPending) {
    body = (
      <section aria-busy="true" aria-label="Dinner loading">
        <Skeleton className={styles.skeletonHero} />
        <div className={styles.sheet}>
          <Skeleton className={styles.skeletonTitle} />
          <Skeleton />
          <Skeleton />
        </div>
      </section>
    )
  } else {
    // Keyed by the dinner, so moving to another dinner starts its visit
    // afresh: nothing checked, on the first step.
    body =
      layout === 'mobile' ? <Detail dinner={dinner.data} /> : <KitchenReading key={dinner.data.id} dinner={dinner.data} />
  }
  return <div className={`${styles.page} ${styles[layout]}`}>{body}</div>
}
