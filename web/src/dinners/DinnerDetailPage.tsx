import { ChevronLeft, Heart } from 'lucide-react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { ApiError } from '../api/errors.ts'
import { duration, Unavailable } from '../plan/PlanPage.tsx'
import { useBreakpoint } from '../shell/useBreakpoint.ts'
import buttonStyles from '../ui/Button.module.css'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import type { Difficulty } from './api.ts'
import { useDinnerDetail, type DinnerDetail, type Ingredient } from './detail.ts'
import styles from './DinnerDetailPage.module.css'

// REGULAR_DINNERS is Settings → Regular dinners, where a recipe is fixed.
export const REGULAR_DINNERS = '/settings/regulars'

// ADD_HINT is why Add to this week and the heart are unavailable.
// TODO(07423ba7): the add-to-week sheet and the favorite heart.
const ADD_HINT = 'Adding to this week and the favorite heart arrive in the next update.'

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

// Recipe is the dinner's ingredients and steps, or what stands in for them:
// still searching, or the link to fix a recipe that could not be found.
function Recipe({ dinner }: { dinner: DinnerDetail }) {
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
  if (dinner.grounding_status === 'pending') {
    return <p className={styles.missing}>Still finding the recipe — its ingredients and steps will show here.</p>
  }
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
      {dinner.siteName !== null && <p className={styles.source}>Recipe from {dinner.siteName}</p>}
    </>
  )
}

// ActionBar is the bar held at the bottom of the screen: Add to this week
// and the favorite heart, showing whether the dinner is a favorite. Both
// rest unavailable until the next update wires them.
function ActionBar({ dinner }: { dinner: DinnerDetail }) {
  return (
    <div className={styles.bar}>
      <Unavailable hint={ADD_HINT} className={styles.add}>
        Add to this week
      </Unavailable>
      <IconButton label="Favorite" size={52} aria-pressed={dinner.is_favorite} disabled>
        <Icon icon={Heart} size="nav" className={dinner.is_favorite ? styles.filled : undefined} />
      </IconButton>
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

// DinnerDetailPage is a dinner with its recipe (mock 1k): its photo, or the
// striped placeholder, under a sheet with Regular or Side quest, difficulty
// and time, why it was picked, the ingredients and the steps. A dinner whose
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
    body = <Detail dinner={dinner.data} />
  }
  return <div className={`${styles.page} ${styles[layout]}`}>{body}</div>
}
