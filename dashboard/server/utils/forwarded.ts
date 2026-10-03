import { isIP } from 'node:net'

/**
 * The address a request to the dashboard came from, for the agent's audit
 * trail: the agent sees only the dashboard's own address, so the proxy passes
 * the browser's on as X-Forwarded-For. Pure, so the rule is unit-tested.
 *
 * A reverse proxy in front of the dashboard (Caddy, in a Shipwick setup)
 * connects from a private or loopback address and appends the address it saw
 * to X-Forwarded-For: its last entry is believed. A peer with a public
 * address is the browser itself, and whatever header it sends is ignored —
 * anyone can write one.
 */
export function clientAddress(forwardedFor: string | string[] | undefined, peer: string | undefined): string {
  const direct = normalize(peer ?? '')
  if (direct !== '' && !isPrivate(direct)) return direct

  const header = Array.isArray(forwardedFor) ? forwardedFor.join(',') : forwardedFor ?? ''
  const entries = header.split(',').map(entry => normalize(entry.trim())).filter(entry => entry !== '')
  return entries[entries.length - 1] ?? direct
}

/** An IP address without the IPv4-mapped prefix; "" for anything that is not one. */
function normalize(address: string): string {
  const plain = address.startsWith('::ffff:') && isIP(address.slice(7)) === 4 ? address.slice(7) : address
  return isIP(plain) === 0 ? '' : plain
}

/** Loopback, RFC 1918, link-local, carrier-grade NAT and the IPv6 equivalents: where a reverse proxy connects from. */
function isPrivate(address: string): boolean {
  if (isIP(address) === 4) {
    const [a = 0, b = 0] = address.split('.').map(Number)
    return a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254) || (a === 100 && b >= 64 && b <= 127)
  }
  const lower = address.toLowerCase()
  return lower === '::1' || lower.startsWith('fc') || lower.startsWith('fd') || lower.startsWith('fe8') || lower.startsWith('fe9') || lower.startsWith('fea') || lower.startsWith('feb')
}
