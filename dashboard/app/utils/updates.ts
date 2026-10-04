import type { Server, UpdateStatus } from '~/types/api'
import { formatRelativeTime } from '~/utils/format'

/**
 * A newer release, as the agent heard of it: the agent asks GitHub once a
 * day and says the answer on GET /server. The dashboard never asks itself; it
 * only words what the agent knows. Pure, so the wording is unit-tested.
 */

/** Where a release is described. The version is the agent's own word for it, and is encoded all the same. */
export function releaseUrl(version: string): string {
  return `https://github.com/shipwick/shipwick/releases/tag/${encodeURIComponent(version)}`
}

/** How a server is brought to the newer release, whichever way it was installed: the agent does not say which. */
export const UPGRADE_GUIDE_URL = 'https://shipwick.com/docs/tasks/upgrade'

/** The installer's line, for a server that was installed with it. */
export const UPGRADE_COMMAND = 'curl -fsSL https://get.shipwick.com | sh'

export interface UpdateNotice {
  latest: string
  running: string
  /** "Shipwick v0.7.1 is available. This server runs v0.7.0." */
  sentence: string
  url: string
}

/** The notice for a server whose agent knows of a newer release; null when there is none to show. */
export function updateNotice(server: Pick<Server, 'agent_version' | 'update'> | null | undefined): UpdateNotice | null {
  const update = server?.update
  if (!server || !update?.available || update.latest_version === '') return null
  return {
    latest: update.latest_version,
    running: server.agent_version,
    sentence: `Shipwick ${update.latest_version} is available. This server runs ${server.agent_version}.`,
    url: releaseUrl(update.latest_version),
  }
}

/**
 * The quiet line next to the agent's version when there is nothing to do:
 * "Up to date, checked 3h ago", or that the check is off. "" when the agent
 * has never been answered — it cannot reach GitHub, and says nothing — and
 * for an agent that does not check at all.
 */
export function updateLine(update: UpdateStatus | undefined, now: number = Date.now()): string {
  if (!update || update.available) return ''
  if (!update.enabled) return 'The check for newer releases is off (SHIPWICK_UPDATE_CHECK)'
  if (!update.checked_at) return ''
  return `Up to date, checked ${formatRelativeTime(update.checked_at, now)}`
}

/**
 * The agent is 0.7 or newer: it says so by what GET /server carries. What it
 * added — a configuration that can be edited whatever the application is, the
 * log archive — is offered only then.
 */
export function atLeast07(server: Pick<Server, 'update' | 'log_archive'> | null | undefined): boolean {
  return server?.update !== undefined || server?.log_archive !== undefined
}
