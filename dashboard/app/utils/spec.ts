import type { Application, AppSpec, SpecBuild, SpecHealth, SpecPublish, SpecSecurity, StaticFiles } from '~/types/api'
import { formatBytes, pluralize } from '~/utils/format'

/**
 * Display helpers for the parts of a deploy.yaml the agent hands back. Pure,
 * so the wording is unit-tested and shared by the application and deployment
 * pages. Where the CLI prints the same thing (`shipwick deploy`'s summary),
 * the wording is the CLI's.
 */

/**
 * An argv joined for display: entries containing whitespace or quotes are
 * quoted, so `["sh", "-c", "echo hi"]` reads `sh -c "echo hi"`. Display only:
 * nothing is ever executed from it.
 */
export function formatArgv(argv: readonly string[] | undefined | null): string {
  if (!argv || argv.length === 0) return ''
  return argv.map((arg) => {
    if (arg === '') return '""'
    if (!/[\s"']/.test(arg)) return arg
    return `"${arg.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
  }).join(' ')
}

export interface HealthDescription {
  /** The check itself: "GET /health", "TCP :5432", "command pg_isready -U postgres". */
  check: string
  /** Its schedule: "every 10s (timeout 3s, 3 retries)", with "after a 1m start period" when a replica gets one. */
  schedule: string
}

/** Renders a health check by kind; null without one. */
export function describeHealth(health: SpecHealth | null | undefined): HealthDescription | null {
  if (!health) return null
  let check: string
  if (health.command && health.command.length > 0) check = `command ${formatArgv(health.command)}`
  else if (health.tcp) check = `TCP :${health.tcp}`
  else check = `GET ${health.path ?? '/'}`
  const retries = `${health.retries} ${health.retries === 1 ? 'retry' : 'retries'}`
  // Failed checks only count once the start period is over: a slow starter is not restarted for being slow.
  const start = health.start_period ? `, after a ${tidyGoDuration(health.start_period)} start period` : ''
  return { check, schedule: `every ${health.interval} (timeout ${health.timeout}, ${retries})${start}` }
}

/** "2m0s" → "2m", "1h0m0s" → "1h", "1m30s" stays; what Go prints, with the zero parts dropped. */
function tidyGoDuration(duration: string): string {
  const tidy = duration.replace(/(?<=\d[hm])0[ms](?=\d*[ms]?)/g, '').replace(/(?<=\d[hm])0s$/, '').replace(/(?<=\dh)0m$/, '')
  return tidy || duration
}

/** "42 files, 3.1 MB, served by the proxy": what a static deployment serves, in the CLI's words. */
export function describeStatic(files: StaticFiles | null | undefined): string {
  if (!files) return ''
  return `${pluralize(files.files, 'file')}, ${formatBytes(files.size_bytes)}, served by the proxy`
}

/** "built by shipwick deploy from . (Dockerfile)": where a `build` application's image comes from. */
export function describeBuild(build: SpecBuild | null | undefined): string {
  if (!build) return ''
  return `built by shipwick deploy from ${build.context} (${build.dockerfile})`
}

/** Whether an image was built by the CLI and sent to the server: nothing to link to, nothing the agent pulls. */
export function isLocalImage(image: string): boolean {
  return image.startsWith('shipwick.local/')
}

/** "5432/tcp → server port 15432 on 10.0.0.5"; without `address` the port is bound on every address. */
export function formatPublish(entry: SpecPublish): string {
  const where = entry.address ? ` on ${entry.address}` : ''
  return `${entry.port}/${entry.protocol} → server port ${entry.host}${where}`
}

export type HostnameKind = 'domain' | 'alias' | 'redirect'

export interface Hostname {
  host: string
  kind: HostnameKind
  /**
   * What the application serves there, as it is written and linked:
   * `example.com/api` for an application with a `path`, the bare hostname
   * otherwise. A redirect is a whole hostname, whatever the path.
   */
  address: string
  /** `https://` + address; null for a wildcard, which is a pattern and not an address. */
  url: string | null
  /** For a redirect: where it sends the browser (the domain). */
  target: string | null
}

/** `*.example.com`: every name one label below, served alike. Not something a browser can open. */
export function isWildcard(hostname: string): boolean {
  return hostname.startsWith('*.')
}

/** "example.com/api": a hostname with the part of it an application serves; the bare hostname without a path. */
export function addressOf(hostname: string, path: string | null | undefined): string {
  return `${hostname}${path ?? ''}`
}

/** Where an application answers, as a link; null without a domain and for a wildcard domain. */
export function applicationUrl(app: Pick<Application, 'domain' | 'path'>): string | null {
  if (!app.domain || isWildcard(app.domain)) return null
  return `https://${addressOf(app.domain, app.path)}`
}

type Routed = Pick<Application, 'domain' | 'aliases' | 'redirects' | 'path'> | Pick<AppSpec, 'domain' | 'aliases' | 'redirects' | 'path'>

/**
 * Every hostname an application answers on, the domain first, then its
 * aliases (served alike) and its redirects (308 to the domain). The domain
 * and the aliases carry the application's `path`; a redirect hostname is
 * taken whole and sends the browser to the same path on the domain. Empty
 * when the application has no domain.
 */
export function hostnamesOf(app: Routed): Hostname[] {
  const domain = app.domain ?? ''
  if (domain === '') return []
  const served = (host: string, kind: HostnameKind): Hostname => {
    const address = addressOf(host, app.path)
    return { host, kind, address, url: isWildcard(host) ? null : `https://${address}`, target: null }
  }
  return [
    served(domain, 'domain'),
    ...(app.aliases ?? []).map(host => served(host, 'alias')),
    ...(app.redirects ?? []).map<Hostname>(host => ({ host, kind: 'redirect', address: host, url: `https://${host}`, target: domain })),
  ]
}

/** "www.example.com → example.com" for a redirect, the address otherwise. */
export function formatHostname(entry: Hostname): string {
  return entry.target ? `${entry.host} → ${entry.target}` : entry.address
}

/** "GET /old → /new (308)": one of the proxy block's redirects. */
export function formatPathRedirect(redirect: { from: string, to: string, status: number }): string {
  return `${redirect.from} → ${redirect.to} (${redirect.status})`
}

/** Whether a proxy block says anything: an empty one is the same as none. */
export function hasProxySettings(spec: Pick<AppSpec, 'proxy'> | null | undefined): boolean {
  const p = spec?.proxy
  if (!p) return false
  return Boolean(p.strip_prefix) || Object.keys(p.headers ?? {}).length > 0 || (p.basic_auth?.length ?? 0) > 0 || (p.redirects?.length ?? 0) > 0
}

/**
 * The `security` block, one line per thing the containers go without, in the
 * order deploy.yaml names them. Empty without the block. `capabilities` has
 * three states and only two of them say anything: absent, the containers keep
 * Docker's default set and there is no line; `[]`, they keep none.
 */
export function describeSecurity(security: SpecSecurity | null | undefined): string[] {
  if (!security) return []
  const lines: string[] = []
  if (security.read_only) lines.push('Read-only root filesystem')
  if (security.tmpfs?.length) lines.push(`Scratch space: ${security.tmpfs.map(t => `${t.path} (${formatBytes(t.size_bytes)})`).join(', ')}`)
  if (Array.isArray(security.capabilities)) lines.push(security.capabilities.length === 0 ? 'Capabilities: none' : `Capabilities kept: ${security.capabilities.join(', ')}`)
  if (security.non_root) lines.push('Refuses to run as root')
  return lines
}

/** Docker keeps a local copy of the logs only for these drivers; any other ships them elsewhere. */
const LOCAL_LOG_DRIVERS: ReadonlySet<string> = new Set(['', 'json-file', 'local'])

/** The driver logs are shipped to, or null when Docker keeps them on the server as usual. */
export function shippedLogDriver(spec: Pick<AppSpec, 'logging'> | null | undefined): string | null {
  const driver = spec?.logging?.driver ?? ''
  return LOCAL_LOG_DRIVERS.has(driver) ? null : driver
}

/** "gelf (2 options)", the CLI's line. */
export function describeLogging(spec: Pick<AppSpec, 'logging'> | null | undefined): string {
  const logging = spec?.logging
  if (!logging) return ''
  const count = Object.keys(logging.options ?? {}).length
  if (count === 0) return logging.driver
  return `${logging.driver} (${count} ${count === 1 ? 'option' : 'options'})`
}
