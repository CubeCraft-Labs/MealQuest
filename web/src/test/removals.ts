// A removal's id is random (crypto.randomUUID), so a test's fake server
// keys a removal's requests without it.
const REMOVAL = /^(DELETE \/api\/v1\/dinners\/[^/?]+)\?removal=[0-9a-f-]{36}$/
const WITHDRAW = /^(POST \/api\/v1\/dinners\/[^/]+\/removals\/)[0-9a-f-]{36}(\/withdraw)$/

// removalKey is the "METHOD path" key of a request with any removal id
// taken out: DELETE /api/v1/dinners/{id} for a removal, and POST
// /api/v1/dinners/{id}/removals/{removal}/withdraw for its withdrawal. Any
// other key is unchanged.
export function removalKey(key: string): string {
  const removal = REMOVAL.exec(key)
  if (removal !== null) return removal[1]!
  const withdraw = WITHDRAW.exec(key)
  if (withdraw !== null) return `${withdraw[1]!}{removal}${withdraw[2]!}`
  return key
}

// withdrawKey is the key of dinner id's withdrawals.
export function withdrawKey(id: string): string {
  return `POST /api/v1/dinners/${id}/removals/{removal}/withdraw`
}

// REMOVING_MARK matches a dinner's mark while its removal is unsettled.
export const REMOVING_MARK = /^removing:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
