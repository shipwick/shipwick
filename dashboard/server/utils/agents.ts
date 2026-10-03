/**
 * Which agents the dashboard's server knows, from its environment. Pure, so
 * the parsing is unit-tested; server/utils/agent.ts reads the environment.
 *
 * One server is the usual case and needs nothing new: SHIPWICK_AGENT_URL.
 * Several are listed in SHIPWICK_AGENTS as name=URL pairs:
 *
 *   SHIPWICK_AGENTS="production=http://agent:9000, staging=https://agent.staging.example.com"
 *
 * The names are what the browser sees and what an address carries
 * (`?server=staging`); the URLs stay on this server, except in the error
 * that says which agent could not be reached.
 */

export interface ConfiguredAgent {
  name: string
  /** Base URL without a trailing slash. */
  url: string
}

/** The name of the one server when only SHIPWICK_AGENT_URL is set. */
export const DEFAULT_SERVER = 'default'

export const DEFAULT_AGENT_URL = 'http://127.0.0.1:9000'

/** A sign-in is a cookie per server, and a browser sends them all with every request. */
export const MAX_AGENTS = 20

const SERVER_NAME = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/

export function isServerName(value: unknown): value is string {
  return typeof value === 'string' && SERVER_NAME.test(value)
}

/** A problem with the configuration, worded for whoever set the variable. Never quotes a URL's credentials. */
export class AgentsConfigError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'AgentsConfigError'
  }
}

/** The origin and path of an agent URL, without a trailing slash. `variable` names where it came from. */
export function normalizeAgentUrl(raw: string, variable: string): string {
  let url: URL
  try {
    url = new URL(raw)
  }
  catch {
    throw new AgentsConfigError(`${variable} is not a valid URL: ${redact(raw)}`)
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new AgentsConfigError(`${variable} must be http or https: ${redact(raw)}`)
  }
  return url.origin + url.pathname.replace(/\/+$/, '')
}

/**
 * The configured agents, in the order they were written. SHIPWICK_AGENTS wins
 * over SHIPWICK_AGENT_URL when both are set; with neither, the agent on
 * loopback.
 */
export function parseAgents(agents: string | undefined, agentUrl: string | undefined): ConfiguredAgent[] {
  const list = (agents ?? '').trim()
  if (list === '') {
    return [{ name: DEFAULT_SERVER, url: normalizeAgentUrl(agentUrl?.trim() || DEFAULT_AGENT_URL, 'SHIPWICK_AGENT_URL') }]
  }

  const result: ConfiguredAgent[] = []
  for (const pair of list.split(/[,\s]+/).filter(p => p !== '')) {
    const cut = pair.indexOf('=')
    if (cut <= 0) {
      throw new AgentsConfigError(`SHIPWICK_AGENTS: "${redact(pair)}" is not a name=URL pair, e.g. production=http://agent:9000`)
    }
    const name = pair.slice(0, cut)
    if (!isServerName(name)) {
      throw new AgentsConfigError(`SHIPWICK_AGENTS: invalid server name "${name}": use lowercase letters, digits and dashes (max 40 characters), e.g. production`)
    }
    if (result.some(a => a.name === name)) {
      throw new AgentsConfigError(`SHIPWICK_AGENTS: the name "${name}" is used twice`)
    }
    result.push({ name, url: normalizeAgentUrl(pair.slice(cut + 1), `SHIPWICK_AGENTS (${name})`) })
  }
  if (result.length > MAX_AGENTS) {
    throw new AgentsConfigError(`SHIPWICK_AGENTS lists ${result.length} servers; at most ${MAX_AGENTS} are supported`)
  }
  return result
}

/** The cookie that holds the sign-in for one server. With a single server it is the name it always had. */
export function sessionCookieFor(agent: Pick<ConfiguredAgent, 'name'>, agents: readonly ConfiguredAgent[]): string {
  return agents.length === 1 ? 'shipwick_session' : `shipwick_session_${agent.name}`
}

/** A URL may carry a user and a password; a message about it must not. */
function redact(raw: string): string {
  return raw.replace(/\/\/[^/@\s]*@/, '//…@')
}
