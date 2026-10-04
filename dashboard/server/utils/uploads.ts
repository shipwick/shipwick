import type { Server } from 'node:http'
import type { H3Event } from 'h3'

/**
 * Node's HTTP server gives every request five minutes to arrive, body
 * included (`requestTimeout`), and answers 408 to one that takes longer. That
 * is right for JSON and wrong for a file of gigabytes on a slow line: a
 * volume archive or an export uploaded through the proxy would be cut at the
 * five-minute mark whatever its size.
 *
 * The limit belongs to the server, not to a request, so it is lifted while an
 * upload that is passed on as it arrives is in flight, and put back when the
 * last one has ended. An upload that stalls is ended by the agent, which
 * gives a read of an archive one minute; a request that carries JSON keeps
 * the limit at every other time.
 */
let uploads = 0
let lifted: { server: Server, was: number } | null = null

/** Call when a streamed upload begins; the function it returns is called once, when the upload has ended however it ended. */
export function holdRequestTimeout(event: H3Event): () => void {
  const server = (event.node.req.socket as { server?: Server } | undefined)?.server
  if (!server || typeof server.requestTimeout !== 'number') return () => {}
  if (uploads === 0) {
    lifted = { server, was: server.requestTimeout }
    server.requestTimeout = 0
  }
  uploads++
  let released = false
  return () => {
    if (released) return
    released = true
    uploads--
    if (uploads === 0 && lifted) {
      lifted.server.requestTimeout = lifted.was
      lifted = null
    }
  }
}
