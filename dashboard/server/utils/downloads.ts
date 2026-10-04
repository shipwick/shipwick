import { createHash, randomBytes } from 'node:crypto'
import type { IncomingMessage } from 'node:http'
import type { H3Event } from 'h3'
import type { ConfiguredAgent } from './agents'
import { DOWNLOAD_CLAIM_MS, EXPORT_ANSWER_TIMEOUT_MS } from './timeouts'

/**
 * An export of the server, downloaded by the browser.
 *
 * The agent takes the passphrase in the body of a POST and answers with the
 * file. A browser saves a file to disk as it arrives only when it fetches it
 * itself, by following a link — and a link is a GET, which cannot carry the
 * passphrase anywhere but in its address. So the download is two requests:
 *
 *   POST …/export            the page sends the passphrase, with the CSRF
 *                            header like every request that changes something.
 *                            The agent is asked; a refusal is answered as it
 *                            is. An export that begins is held here, unread,
 *                            and the page gets a ticket.
 *   GET  …/export/<ticket>   the browser follows a link, and the held answer
 *                            is piped into it as a download.
 *
 * Nothing of the file is read before the browser asks for it, and then it is
 * passed on as it arrives: no memory, no disk. The passphrase is forwarded
 * and forgotten. A ticket is random, works once, only for the session that
 * asked for it, and for half a minute; an export nobody fetches is ended.
 * Tickets live in this process: with several dashboard servers behind one
 * address the two requests must reach the same one.
 */
interface HeldDownload {
  upstream: IncomingMessage
  owner: string
  timer: ReturnType<typeof setTimeout>
}

const held = new Map<string, HeldDownload>()

/** More than a few at once is not somebody downloading exports. */
const MAX_HELD = 4

const ownerOf = (agent: ConfiguredAgent, token: string) => `${agent.name}\n${createHash('sha256').update(token).digest('hex')}`

/** The file name of a Content-Disposition header, for the page to show; "" when it names none that is safe to show. */
export function attachmentName(header: string | string[] | undefined): string {
  const match = /filename="([\w.-]{1,128})"/.exec(String(header ?? ''))
  return match ? match[1]! : ''
}

/** POST: asks the agent for an export and holds its answer for the browser to fetch. */
export async function startExportDownload(event: H3Event, server: string | null): Promise<unknown> {
  try {
    const agent = agentFor(server)
    const cookie = sessionCookieName(agent)
    assertSameOriginRequest(event)
    const token = getCookie(event, cookie)
    if (!token || !isPlausibleToken(token)) throw new AgentProxyError(401, 'UNAUTHORIZED', 'Not signed in')
    if (held.size >= MAX_HELD) throw new AgentProxyError(429, 'INVALID_REQUEST', 'Several exports were started and not fetched. Wait half a minute and try again.')

    const body = await readSmallBody(event)
    const upstream = await agentRequest({
      agent,
      method: 'POST',
      path: `${AGENT_API_PREFIX}export`,
      token,
      headers: forwardedHeaders(event),
      body,
      headersTimeoutMs: EXPORT_ANSWER_TIMEOUT_MS,
    })

    if (upstream.statusCode !== 200) {
      // A refusal, in the agent's own envelope: passed on as any other answer is.
      if (upstream.statusCode === 401) deleteCookie(event, cookie, sessionCookieOptions(event))
      await relayResponse(event, upstream)
      return
    }

    const ticket = randomBytes(24).toString('base64url')
    const timer = setTimeout(() => {
      // Nobody came for it: the agent is told by the connection that ends.
      held.delete(ticket)
      upstream.destroy()
    }, DOWNLOAD_CLAIM_MS)
    timer.unref()
    upstream.once('close', () => {
      if (held.get(ticket)?.upstream === upstream) {
        clearTimeout(timer)
        held.delete(ticket)
      }
    })
    held.set(ticket, { upstream, owner: ownerOf(agent, token), timer })

    setResponseHeader(event, 'cache-control', 'no-store')
    return { data: { ticket, filename: attachmentName(upstream.headers['content-disposition']) } }
  }
  catch (error) {
    if (event.node.res.headersSent) {
      event.node.res.destroy()
      return
    }
    return sendProxyError(event, error)
  }
}

/** GET: the browser fetches the export a ticket stands for; it is piped into the download. */
export async function claimExportDownload(event: H3Event, server: string | null): Promise<unknown> {
  try {
    const agent = agentFor(server)
    const token = getCookie(event, sessionCookieName(agent))
    if (!token || !isPlausibleToken(token)) throw new AgentProxyError(401, 'UNAUTHORIZED', 'Not signed in')

    const ticket = getRouterParam(event, 'ticket') ?? ''
    const entry = held.get(ticket)
    if (!entry || entry.owner !== ownerOf(agent, token)) {
      throw new AgentProxyError(404, 'DOWNLOAD_EXPIRED', 'This download is no longer waiting: it is fetched within half a minute of being started, and once. Start the export again.')
    }
    held.delete(ticket)
    clearTimeout(entry.timer)

    // The browser going away ends the export on the agent, too.
    event.node.res.once('close', () => entry.upstream.destroy())
    await relayResponse(event, entry.upstream)
  }
  catch (error) {
    if (event.node.res.headersSent) {
      event.node.res.destroy()
      return
    }
    return sendProxyError(event, error)
  }
}
