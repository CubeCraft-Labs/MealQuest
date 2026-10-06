import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'

export const DINNERS_PATH = '/api/v1/dinners'
export const REGULARS_PATH = `${DINNERS_PATH}?status=regular`

// Regular is the part of a dinner the Regulars list reads. pending is true
// while an optimistic add waits for the server; its id is not the server's.
export type Regular = { id: string; name: string; pending?: true }

// REGULARS_KEY prefixes every Regulars query, so a change refreshes them all
// (the settings home's count included).
export const REGULARS_KEY = ['dinners', 'regular'] as const
const REGULARS_LIST_KEY = [...REGULARS_KEY, 'list'] as const
const REGULARS_MUTATION_KEY = ['dinners', 'regular', 'change'] as const

function parseDinner(where: string, body: unknown): Regular {
  if (!isRecord(body) || typeof body.id !== 'string' || typeof body.name !== 'string') {
    throw new Error(`${where}: response is not a dinner`)
  }
  return { id: body.id, name: body.name }
}

// useRegulars is the household's Regulars, in the server's order.
export function useRegulars() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: REGULARS_LIST_KEY,
    queryFn: async () => {
      const where = `GET ${REGULARS_PATH}`
      const body = await apiFetch<unknown>(REGULARS_PATH)
      if (!isRecord(body) || !Array.isArray(body.dinners)) throw new Error(`${where}: response has no dinners list`)
      return body.dinners.map((dinner) => parseDinner(where, dinner))
    },
  })
}

// changeOptimistically stops Regulars reads in flight, so they cannot
// overwrite the optimistic list, then applies change to it. It returns the
// list as it was.
async function changeOptimistically(queryClient: QueryClient, change: (list: Regular[]) => Regular[]) {
  await queryClient.cancelQueries({ queryKey: REGULARS_LIST_KEY })
  const before = queryClient.getQueryData<Regular[]>(REGULARS_LIST_KEY) ?? []
  queryClient.setQueryData<Regular[]>(REGULARS_LIST_KEY, change(before))
  return before
}

// settle refetches the Regulars once the last change in flight is done, so
// an earlier change's refetch never drops a later change's optimistic entry.
function settle(queryClient: QueryClient) {
  if (queryClient.isMutating({ mutationKey: REGULARS_MUTATION_KEY }) === 1) {
    void queryClient.invalidateQueries({ queryKey: REGULARS_KEY })
  }
}

let pendingIds = 0

// useAddRegular adds a Regular by name (POST /api/v1/dinners). It shows in
// the list at once and is taken out again if the server refuses it.
export function useAddRegular() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async (name: string) =>
      parseDinner(`POST ${DINNERS_PATH}`, await apiFetch<unknown>(DINNERS_PATH, { method: 'POST', body: { name } })),
    onMutate: async (name) => {
      const id = `pending-${++pendingIds}`
      await changeOptimistically(queryClient, (list) => [...list, { id, name, pending: true }])
      return { id }
    },
    onSuccess: (dinner, _name, context) => {
      queryClient.setQueryData<Regular[]>(REGULARS_LIST_KEY, (list) =>
        (list ?? []).map((regular) => (regular.id === context.id ? dinner : regular)),
      )
    },
    onError: (_error, _name, context) => {
      if (context === undefined) return
      queryClient.setQueryData<Regular[]>(REGULARS_LIST_KEY, (list) =>
        (list ?? []).filter((regular) => regular.id !== context.id),
      )
    },
    onSettled: () => settle(queryClient),
  })
}

// useRemoveRegular archives a Regular (DELETE /api/v1/dinners/{id}). It
// leaves the list at once and is put back if the server refuses.
export function useRemoveRegular() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: REGULARS_MUTATION_KEY,
    mutationFn: async (regular: Regular) => {
      await apiFetch<unknown>(`${DINNERS_PATH}/${encodeURIComponent(regular.id)}`, { method: 'DELETE' })
    },
    onMutate: async (regular) => {
      const before = await changeOptimistically(queryClient, (list) => list.filter((r) => r.id !== regular.id))
      return { index: before.findIndex((r) => r.id === regular.id) }
    },
    onError: (_error, regular, context) => {
      if (context === undefined) return
      queryClient.setQueryData<Regular[]>(REGULARS_LIST_KEY, (list) => {
        const next = [...(list ?? [])]
        next.splice(context.index === -1 ? next.length : context.index, 0, regular)
        return next
      })
    },
    onSettled: () => settle(queryClient),
  })
}
