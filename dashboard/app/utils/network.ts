import type { NetworkStatus } from '~/types/api'

/**
 * How the agent leaves the server, for the server page: said only when
 * something was set, in the words of `shipwick server status`. Pure, so the
 * wording is unit-tested.
 */
export interface NetworkDescription {
  /** "proxy proxy.example.com:3128", "certificate authorities of its own", "DNS system", "certificates from https://…". */
  parts: string[]
  /** What will go wrong as things are, and what to set; "" when nothing will. */
  warning: string
}

/** Null on a server with nothing set, and for an agent that does not say. */
export function describeNetwork(network: NetworkStatus | null | undefined): NetworkDescription | null {
  if (!network) return null
  const parts: string[] = []
  if (network.proxy) parts.push(`proxy ${network.proxy}`)
  if (network.ca_file) parts.push('certificate authorities of its own')
  if (network.dns_resolvers?.length) parts.push(`DNS ${network.dns_resolvers.join(', ')}`)
  if (network.acme_directory) parts.push(`certificates from ${network.acme_directory}`)
  if (parts.length === 0) return null
  return {
    parts,
    // The agent's own requests go through the proxy; an image is pulled by the Docker daemon, which has its own setting.
    warning: network.proxy && !network.docker_proxy
      ? 'Images are pulled by the Docker daemon, and it has no proxy configured: pulls will not get out. Set "proxies" in /etc/docker/daemon.json on the server and restart Docker.'
      : '',
  }
}
