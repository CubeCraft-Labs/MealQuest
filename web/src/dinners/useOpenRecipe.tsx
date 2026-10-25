import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { ApiError, useApi } from '../api/index.ts'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { dinnerDetailKey } from './api.ts'
import { checkDinnerDetail } from './detail.ts'

// RecipeDinner is the dinner whose recipe a plan day, Home, a Supply run, a
// Regular, a Quest-log row, a Recent update or a notification offers.
export type RecipeDinner = { id: string; name: string }

// RecipeCheck is the last recipe opened that did not open: gone when the
// server answered that the dinner has no detail (404), failed when it could
// not be asked or its answer could not be read.
type RecipeCheck = { dinner: RecipeDinner; outcome: 'gone' } | { dinner: RecipeDinner; outcome: 'failed'; error: Error }

export type OpenRecipe = {
  // open asks the server for dinner's recipe and opens its detail only once
  // the server has answered with it. While one check is out, open does
  // nothing.
  open: (dinner: RecipeDinner) => void
  // notice says, in place, why the last recipe opened did not open, or is
  // null.
  notice: ReactNode
}

// useOpenRecipe is the one way the app opens a dinner's recipe from a plan
// day, Home, a Supply run, the Regulars, the Quest log, a Recent update or a
// notification. Any of those may have been read or sent before another
// device archived the dinner, even while the page stayed open, so its recipe
// opens only from the server's answer when it is activated (checkDinnerDetail): never from the service worker's
// cache, nor from a detail this page read before, which may still be fresh
// here. That answer is what the detail shows. A 404 opens nothing: no read
// here offers the recipe any more, and notice says the dinner was removed.
// A check that fails opens nothing either, and notice offers to try again.
// Leaving the page abandons a check still out.
export function useOpenRecipe(): OpenRecipe {
  const { apiCheck } = useApi()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [check, setCheck] = useState<RecipeCheck | null>(null)
  const checking = useRef<AbortController | null>(null)
  useEffect(
    () => () => {
      checking.current?.abort()
      checking.current = null
    },
    [],
  )

  const open = useCallback(
    (dinner: RecipeDinner) => {
      if (checking.current !== null) return
      const controller = new AbortController()
      checking.current = controller
      setCheck(null)
      checkDinnerDetail(apiCheck, queryClient, dinner.id, controller.signal).then(
        (detail) => {
          if (checking.current === controller) checking.current = null
          if (controller.signal.aborted) return
          queryClient.setQueryData(dinnerDetailKey(dinner.id), detail)
          void navigate(`/dinner/${encodeURIComponent(dinner.id)}`)
        },
        (error: unknown) => {
          if (checking.current === controller) checking.current = null
          if (controller.signal.aborted) return
          if (error instanceof ApiError && error.status === 404) setCheck({ dinner, outcome: 'gone' })
          else setCheck({ dinner, outcome: 'failed', error: error instanceof Error ? error : new Error(String(error)) })
        },
      )
    },
    [apiCheck, queryClient, navigate],
  )

  let notice: ReactNode = null
  if (check?.outcome === 'gone') {
    notice = <ErrorBanner>{`${check.dinner.name} was removed, so its recipe is gone.`}</ErrorBanner>
  } else if (check?.outcome === 'failed') {
    const { dinner, error } = check
    notice = (
      <ErrorBanner onRetry={() => open(dinner)}>
        {`Couldn't check the recipe for ${dinner.name} with the server, so it didn't open. ${error.message}`}
      </ErrorBanner>
    )
  }
  return { open, notice }
}
