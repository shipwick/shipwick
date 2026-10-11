import type { AgentEvent, Deployment } from '~/types/api'
import type { Finding } from '~/utils/diagnosis'

/**
 * The agent's one look at a deployed application through the proxy: from 0.9
 * on, once a container deployment with a routed domain is complete, it asks
 * `https://<domain><path>/` the way a visitor would and records what it found
 * as one `probe` event — after `completed_at`, the only event that comes
 * then. At level `warn` it is something to fix (a redirect loop, a proxy that
 * could not be asked); at `info`, what the hostname answered. Pure, so the
 * rules are unit-tested.
 */

/** The probe event of a deployment, or null while there is none. */
export function probeOf(events: readonly AgentEvent[] | null | undefined): AgentEvent | null {
  let found: AgentEvent | null = null
  for (const event of events ?? []) if (event.type === 'probe' && (!found || event.id > found.id)) found = event
  return found
}

/**
 * How long after completion a probe may still come: the agent asks again
 * every ten seconds for up to three minutes while the proxy cannot answer
 * (a certificate on its way). Past this, nothing is waited for: an agent
 * before 0.9 never writes one, and a page must not keep asking for it.
 */
export const PROBE_WINDOW_MS = 5 * 60 * 1000

/** When a page that stopped polling at completion asks once more, and once again, for the probe. */
export const PROBE_RECHECKS_MS: readonly number[] = [6000, 30_000]

/** The step the agent records when the proxy routes the hostname: the one deployment a probe follows. */
const ROUTED = /^Routed https:\/\//

/**
 * Whether a probe event is still to be expected of a deployment: it is
 * complete and succeeded, the proxy routed a hostname for it, no probe has
 * come yet, and it completed recently enough for one to come.
 */
export function probeExpected(deployment: Pick<Deployment, 'status' | 'completed_at' | 'static'> & { events?: readonly AgentEvent[] | null }, now: number = Date.now()): boolean {
  // A folder is served by the proxy itself; the agent asks nothing of it.
  if (!deployment.completed_at || deployment.static) return false
  if (deployment.status !== 'ACTIVE' && deployment.status !== 'SUPERSEDED') return false
  const events = deployment.events ?? []
  if (probeOf(events)) return false
  if (!events.some(e => e.type === 'step' && ROUTED.test(e.message))) return false
  const completed = Date.parse(deployment.completed_at)
  return !Number.isNaN(completed) && now - completed < PROBE_WINDOW_MS
}

/**
 * A `warn` probe as a finding of the application's page, while that
 * deployment is active: the agent's sentence as it wrote it, since it already
 * says what to do. Null for no probe and for one that found nothing wrong.
 */
export function probeFinding(event: AgentEvent | null, deployment: Pick<Deployment, 'id' | 'sequence'>): Finding | null {
  if (!event || event.level !== 'warn') return null
  return {
    key: `probe-${deployment.id}`,
    tone: 'warn',
    title: 'After the deployment, the agent asked the application through the proxy and found something to fix',
    detail: event.message,
    action: { label: `Open deployment #${deployment.sequence}`, to: `/deployments/${deployment.id}` },
  }
}
