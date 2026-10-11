import type { CloudflareStatus } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import type { StatusDisplay } from '~/utils/status'

/**
 * What the agent found out about its Cloudflare token, for the server page.
 * The agent checks the token the way the proxy uses it — from the server's
 * own address, in every zone it sees, by writing and removing a TXT record —
 * and the result carries sentences a person acts on. Nothing of the token is
 * in it, and nothing of it is said here. Pure, so the wording is unit-tested.
 */

/** The badge of the Cloudflare block. */
export function cloudflareDisplay(status: Pick<CloudflareStatus, 'ok'>): StatusDisplay {
  return status.ok
    ? { tone: 'ok', label: 'Token works' }
    : { tone: 'danger', label: 'Token cannot do its job' }
}

/** "can edit the DNS records of example.com and shop.example" / "sees no zone": the one line under the badge. */
export function cloudflareSummary(status: Pick<CloudflareStatus, 'ok' | 'zones'>): string {
  const names = status.zones.map(z => z.name)
  if (names.length === 0) return 'The token sees no zone.'
  if (status.ok) return `The token can edit the DNS records of ${names.join(', ')}.`
  const editable = status.zones.filter(z => z.dns_edit).map(z => z.name)
  return editable.length === 0
    ? `The token sees ${names.join(', ')} and can edit the DNS records of none of them.`
    : `The token can edit the DNS records of ${editable.join(', ')}, and not of ${names.filter(n => !editable.includes(n)).join(', ')}.`
}

/** How a zone's mark reads: whether the agent could write a record in it. */
export function zoneDisplay(zone: { dns_edit: boolean }): StatusDisplay {
  return zone.dns_edit ? { tone: 'ok', label: 'DNS edit' } : { tone: 'danger', label: 'No DNS edit' }
}

export interface VerifyRefusal {
  /** Hide the button for good: the agent has no token (409), or is older than the endpoint (404). */
  hide: boolean
  /** What to say instead of a result; "" when the error speaks for itself. */
  message: string
  /** Seconds until the agent takes another check, from a 429; 0 otherwise. */
  retryAfterSeconds: number
}

/**
 * What a refused check means for the page. A check writes a record in every
 * zone, so the agent takes one a minute: the 429 says when, and `GET /server`
 * has the last result, which stays on screen. An agent without a token, or
 * one that does not know the endpoint, is not asked again.
 */
export function verifyRefusal(error: Pick<AgentError, 'code' | 'status' | 'details' | 'retryAfter' | 'message'>): VerifyRefusal {
  if (error.code === 'CLOUDFLARE_NOT_CONFIGURED' || error.code === 'ENDPOINT_NOT_FOUND') {
    return {
      hide: true,
      message: error.code === 'ENDPOINT_NOT_FOUND'
        ? 'This server\'s agent is older than 0.9 and does not check its token on request.'
        : 'The agent has no Cloudflare token to check.',
      retryAfterSeconds: 0,
    }
  }
  if (error.code === 'CLOUDFLARE_CHECKED_RECENTLY' || error.status === 429) {
    const fromDetails = Number(error.details.retry_after_seconds)
    const seconds = error.retryAfter ?? (Number.isFinite(fromDetails) && fromDetails > 0 ? Math.ceil(fromDetails) : 60)
    return {
      hide: false,
      message: `Checked less than a minute ago; a check writes a DNS record in every zone, so the agent takes one a minute. The result shown is that check's. Try again in ${seconds} ${seconds === 1 ? 'second' : 'seconds'}.`,
      retryAfterSeconds: seconds,
    }
  }
  return { hide: false, message: '', retryAfterSeconds: 0 }
}
