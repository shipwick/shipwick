import type { VolumeInfo } from '~/types/api'
import { formatBytes } from '~/utils/format'
import type { StatusDisplay } from '~/utils/status'

/**
 * Display rules of the Volumes page: every volume Shipwick created on the
 * server, whether or not its application still exists. Pure, so the wording is
 * unit-tested.
 */

/** "in use" while the application exists; "application deleted" for a volume kept after its deletion. */
export function volumeStatusDisplay(volume: Pick<VolumeInfo, 'orphan'>): StatusDisplay {
  return volume.orphan
    ? { tone: 'muted', label: 'application deleted' }
    : { tone: 'ok', label: 'in use' }
}

/** The daemon reports -1 when it has no size for a volume. */
export function formatVolumeSize(sizeBytes: number): string {
  return sizeBytes < 0 ? 'unknown' : formatBytes(sizeBytes)
}

/** Only a volume whose application is gone can be removed; a live one is replaced through a restore on the application's page. */
export function removable(volume: Pick<VolumeInfo, 'orphan'>): boolean {
  return volume.orphan
}

/** What removing a volume does, for the confirmation: the size when it is known. */
export function volumeRemovalConsequence(volume: Pick<VolumeInfo, 'name' | 'application' | 'size_bytes'>): string {
  const held = volume.size_bytes < 0 ? 'Everything in it' : `The ${formatVolumeSize(volume.size_bytes)} in it`
  return `${held} is deleted with the volume. It belonged to ${volume.application}, which no longer exists; nothing running is affected. This cannot be undone.`
}

/** Orphans first, since they are what this page is for; then by name, the agent's order. */
export function sortVolumes<T extends Pick<VolumeInfo, 'name' | 'orphan'>>(volumes: readonly T[]): T[] {
  return [...volumes].sort((a, b) => Number(b.orphan) - Number(a.orphan) || a.name.localeCompare(b.name))
}
