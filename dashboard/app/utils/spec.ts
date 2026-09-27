import type { Application, AppSpec, SpecHealth, SpecPublish } from '~/types/api'

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
  /** Its schedule: "every 10s (timeout 3s, 3 retries)". */
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
  return { check, schedule: `every ${health.interval} (timeout ${health.timeout}, ${retries})` }
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
  /** For a redirect: where it sends the browser (the domain). */
  target: string | null
}

/**
 * Every hostname an application answers on, the domain first, then its
 * aliases (served alike) and its redirects (308 to the domain). Empty when
 * the application has no domain.
 */
export function hostnamesOf(app: Pick<Application, 'domain' | 'aliases' | 'redirects'> | Pick<AppSpec, 'domain' | 'aliases' | 'redirects'>): Hostname[] {
  const domain = app.domain ?? ''
  if (domain === '') return []
  return [
    { host: domain, kind: 'domain', target: null },
    ...(app.aliases ?? []).map<Hostname>(host => ({ host, kind: 'alias', target: null })),
    ...(app.redirects ?? []).map<Hostname>(host => ({ host, kind: 'redirect', target: domain })),
  ]
}

/** "www.example.com → example.com" for a redirect, the bare hostname otherwise. */
export function formatHostname(entry: Hostname): string {
  return entry.target ? `${entry.host} → ${entry.target}` : entry.host
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
