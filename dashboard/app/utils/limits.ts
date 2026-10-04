import type { DockerStatus, MetricsLimits, Server } from '~/types/api'

/**
 * What the Docker daemon says of itself, and what follows for the limits of
 * `resources`. A daemon without cgroup controllers — rootless Docker without
 * delegation — accepts a limit and applies nothing. Pure, so the wording is
 * unit-tested; where the CLI prints the same thing, the words are the CLI's.
 *
 * An agent before 0.8 says nothing about its daemon. Nothing here reads that
 * silence as "enforced": without the agent's word there is no claim.
 */

const NAMES: Record<string, string> = { memory: 'memory', cpu: 'CPU' }
/** deploy.yaml's order, whatever the agent's. */
const ORDER = ['memory', 'cpu']

function known(limits: readonly string[] | null | undefined): string[] {
  return ORDER.filter(limit => (limits ?? []).includes(limit))
}

function list(limits: readonly string[]): string {
  return limits.map(limit => NAMES[limit] ?? limit).join(' and ')
}

/** "memory and CPU limits are not enforced", "memory limits are not enforced"; "" when the daemon applies both, or did not say. */
export function unenforcedSentence(limits: readonly string[] | null | undefined): string {
  const names = known(limits)
  return names.length === 0 ? '' : `${list(names)} limits are not enforced`
}

export interface DockerDescription {
  /** "29.8.2", "29.8.2, rootless": the version and how the daemon runs. */
  version: string
  /** "Memory and CPU limits are not enforced"; "" when the daemon applies both, or did not say. */
  warning: string
  /** What that means for a deploy.yaml and what to do on the server; "" without a warning. */
  advice: string
}

/** The Docker row of the server's page, in the words of `shipwick server status` and `shipwick doctor`. */
export function describeDocker(server: Pick<Server, 'docker_version' | 'docker'>): DockerDescription {
  const version = server.docker_version || '—'
  const docker = server.docker
  if (!docker) return { version, warning: '', advice: '' }
  const names = known(docker.unenforced_limits)
  const described = { version: docker.rootless ? `${version}, rootless` : version, warning: '', advice: '' }
  if (names.length === 0) return described
  const sentence = unenforcedSentence(names)
  const keys = names.map(name => `resources.${name}`).join(' and ')
  return {
    ...described,
    warning: `${sentence.charAt(0).toUpperCase()}${sentence.slice(1)}`,
    advice: docker.rootless
      ? `No replica is held to ${keys} of its deploy.yaml${names.length === ORDER.length ? ', and the usage shown for a replica is not its own' : ''}. Delegate the cpu and memory cgroup controllers to the user who runs Docker, on a server with systemd (handbook: Rootless Docker).`
      : `No replica is held to ${keys} of its deploy.yaml. The server's kernel offers Docker no cgroup controller for ${names.length === 1 ? 'it' : 'them'}; docker info on the server says which.`,
  }
}

/** What an application asks for per replica, or in sum: 0 is no limit. */
export interface AskedLimits {
  memory: number
  cpu: number
}

/**
 * The limits an application sets that Docker on its server does not apply.
 * `unenforced` is the agent's list (of a metrics sample, or of the server);
 * absent means the agent did not say, and nothing is concluded.
 */
export function unenforcedOf(unenforced: readonly string[] | null | undefined, asked: AskedLimits): string[] {
  return known(unenforced).filter(limit => (limit === 'memory' ? asked.memory > 0 : asked.cpu > 0))
}

/** Whether a limit that is shown is one nothing holds a replica to. */
export function isUnenforced(unenforced: readonly string[] | null | undefined, limit: 'memory' | 'cpu'): boolean {
  return known(unenforced).includes(limit)
}

/**
 * With both limits unenforced the daemon has no cgroups at all, and what it
 * reports as a container's usage is that of everything it runs: not a number
 * to draw as the replica's own.
 */
export function usageIsTheDaemons(unenforced: readonly string[] | null | undefined): boolean {
  return known(unenforced).length === ORDER.length
}

/**
 * The sentence next to an application's limits: "Docker on this server does
 * not enforce the memory and CPU limits: the replicas run without them." ""
 * when every limit the application sets is applied, or the agent did not say.
 */
export function limitsNotice(unenforced: readonly string[] | null | undefined, asked: AskedLimits): string {
  const names = unenforcedOf(unenforced, asked)
  if (names.length === 0) return ''
  return `Docker on this server does not enforce the ${list(names)} ${names.length === 1 ? 'limit' : 'limits'}: the replicas run without ${names.length === 1 ? 'it' : 'them'}.`
}

/** Said in place of the usage where it is not the replica's own. */
export const DAEMON_USAGE_NOTICE = 'Not measured per replica: Docker on this server reports the usage of everything it runs.'

/** A history without the limits nothing is held to: its charts draw no limit line and scale to what was used. */
export function withoutUnenforced<T extends { limits: MetricsLimits }>(history: T, unenforced: readonly string[] | null | undefined): T {
  const names = known(unenforced)
  if (names.length === 0) return history
  return { ...history, limits: { cpu: names.includes('cpu') ? 0 : history.limits.cpu, memory_bytes: names.includes('memory') ? 0 : history.limits.memory_bytes } }
}

/** The agent's list, from whichever answer carries it: a metrics sample first, then the server's own. */
export function unenforcedLimits(sample: { unenforced_limits?: readonly string[] } | null | undefined, docker: DockerStatus | null | undefined): readonly string[] | undefined {
  return sample?.unenforced_limits ?? docker?.unenforced_limits
}
