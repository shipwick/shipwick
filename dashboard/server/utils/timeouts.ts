/**
 * How long the agent may take to answer with headers, by what was asked of it.
 * Pure, so the table is unit-tested; the proxy only looks the answer up.
 */

/** Most requests: the agent answers from its database or with one Docker call. */
export const HEADERS_TIMEOUT_MS = 60_000

/**
 * An archive upload, counted from its last byte: the agent extracts it into
 * the volume before it answers. Also the first byte of a backup's archive,
 * which the agent may have to fetch from the bucket and decrypt first.
 */
export const ARCHIVE_ANSWER_TIMEOUT_MS = 10 * 60_000

/** The longest `deploy.stop_timeout` deploy.yaml accepts. */
const MAX_STOP_TIMEOUT_MS = 10 * 60_000

/**
 * Stopping and deleting answer once every replica has exited. Each gets its
 * `deploy.stop_timeout` after SIGTERM, and a replica a deployment replaced a
 * moment ago may still be using up its own before that: twice the longest
 * grace period, and a minute for the daemon.
 */
export const STOP_ANSWER_TIMEOUT_MS = 2 * MAX_STOP_TIMEOUT_MS + 60_000

/**
 * Promoting a standby answers when the last application it started is ready,
 * or has used up its startup budget: as long as those budgets together.
 */
export const PROMOTE_ANSWER_TIMEOUT_MS = 30 * 60_000

/**
 * An import is read by the agent while it arrives, and answered when every
 * application in it has been dealt with. The clock starts at the upload's
 * last byte: what is left then is the deployment of the applications at the
 * end of the file, which get their startup budgets as in a promotion.
 */
export const IMPORT_ANSWER_TIMEOUT_MS = PROMOTE_ANSWER_TIMEOUT_MS

/**
 * An export answers with its first byte, once the agent has read every
 * application's configuration and begun the first archive.
 */
export const EXPORT_ANSWER_TIMEOUT_MS = ARCHIVE_ANSWER_TIMEOUT_MS

/**
 * How long a started export waits for the browser to come and fetch it: less
 * than the minute after which the agent gives up a write that stalls.
 */
export const DOWNLOAD_CLAIM_MS = 30_000

/**
 * Whether a request's body is passed on as it arrives instead of being read
 * first: every PUT (a volume archive, or a small JSON body), and the one POST
 * that carries a file, an import. Everything else is JSON of a few kilobytes.
 */
export function streamsBody(method: string, segments: readonly string[]): boolean {
  if (method === 'PUT') return true
  return method === 'POST' && segments.length === 1 && segments[0] === 'import'
}

/**
 * The headers timeout for a request, from its method and the decoded path
 * segments below /api/v1 (`['applications', 'web', 'stop']`).
 */
export function answerTimeoutMs(method: string, segments: readonly string[]): number {
  const [collection, , action] = segments
  if (method === 'POST' && segments.length === 2 && collection === 'standby' && segments[1] === 'promote') return PROMOTE_ANSWER_TIMEOUT_MS
  if (method === 'POST' && segments.length === 1 && collection === 'import') return IMPORT_ANSWER_TIMEOUT_MS
  if (method === 'POST' && segments.length === 1 && collection === 'export') return EXPORT_ANSWER_TIMEOUT_MS
  if (collection === 'applications') {
    if (method === 'DELETE' && segments.length === 2) return STOP_ANSWER_TIMEOUT_MS
    if (method === 'POST' && segments.length === 3 && action === 'stop') return STOP_ANSWER_TIMEOUT_MS
    if (method === 'GET' && segments[segments.length - 1] === 'archive') return ARCHIVE_ANSWER_TIMEOUT_MS
  }
  if (method === 'PUT') return ARCHIVE_ANSWER_TIMEOUT_MS
  return HEADERS_TIMEOUT_MS
}

/**
 * Path segments the agent uses are names, numbers and fixed words. Two kinds
 * of name carry one more character: a registry may have a port
 * (`registry.example.com:5000`) and a certificate may be stored under a
 * wildcard (`*.example.com`). Nothing else gets through.
 */
const SAFE_SEGMENT = /^(\*\.)?[\w.~-]+(:\d{1,5})?$/

export function isSafeSegment(segment: string): boolean {
  return segment !== '.' && segment !== '..' && SAFE_SEGMENT.test(segment)
}
