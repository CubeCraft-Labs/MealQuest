import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { archiveTonight } from '../home/api.ts'
import { archivePlanDinner } from '../plan/api.ts'
import { archiveSupplyRunDinner } from '../shopping/api.ts'
import { regularRemoval } from './api.ts'

// useRemoveRegular archives a Regular (DELETE /api/v1/dinners/{id}). It
// leaves the list at once and is put back in its place if the server
// refuses. Once archived it has no detail to open, so every plan, Supply run
// and Home tonight that has it, cached or saved on this device, shows it
// archived: none links to its recipe.
export function useRemoveRegular() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation(
    regularRemoval(queryClient, apiFetch, (id) => {
      archivePlanDinner(queryClient, id)
      archiveSupplyRunDinner(queryClient, id)
      archiveTonight(queryClient, id)
    }),
  )
}
