<script setup lang="ts">
import type { Deployment } from '~/types/api'
import { durationBetween, formatDuration } from '~/utils/format'
import { deploymentStatusDisplay } from '~/utils/status'

const props = withDefaults(defineProps<{
  deployments: Deployment[]
  /** Hide the application column on an application's own page. */
  showApplication?: boolean
}>(), { showApplication: true })

const now = useNow()
const router = useRouter()

function duration(d: Deployment): string {
  if (d.completed_at) return formatDuration(durationBetween(d.started_at, d.completed_at))
  // Still running: count up.
  const started = Date.parse(d.started_at)
  return Number.isNaN(started) ? '—' : formatDuration(Math.max(0, Math.floor((now.value - started) / 1000) * 1000))
}

function hasOrigin(d: Deployment): boolean {
  return d.kind === 'rollback' || d.kind === 'redeploy'
}

function open(d: Deployment, event: MouseEvent) {
  // Let real links inside the row (and text selection) work as usual.
  if ((event.target as HTMLElement).closest('a') || window.getSelection()?.toString()) return
  void router.push(`/deployments/${d.id}`)
}
</script>

<template>
  <table class="data-table stack">
    <thead>
      <tr>
        <th class="w-16">
          No.
        </th>
        <th v-if="props.showApplication">
          Application
        </th>
        <th>Version</th>
        <th>Origin</th>
        <th>Status</th>
        <th>Started</th>
        <th class="right">
          Duration
        </th>
        <th class="w-[30%]">
          Error
        </th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="d in props.deployments" :key="d.id" class="clickable" @click="open(d, $event)">
        <td data-primary>
          <NuxtLink :to="`/deployments/${d.id}`" class="mono link" :aria-label="`Deployment ${d.sequence} of ${d.application}`">#{{ d.sequence }}</NuxtLink>
          <span v-if="props.showApplication" class="mono ml-2 sm:hidden">{{ d.application }}</span>
        </td>
        <td v-if="props.showApplication" class="max-sm:!hidden">
          <NuxtLink :to="`/applications/${d.application}`" class="mono hover:underline">{{ d.application }}</NuxtLink>
        </td>
        <td data-label="Version" class="mono">
          {{ d.version || '—' }}
        </td>
        <td :data-label="hasOrigin(d) ? 'Origin' : undefined" :class="hasOrigin(d) ? '' : 'max-sm:!hidden'">
          <DeploymentOrigin :deployment="d" :known="props.deployments" />
        </td>
        <td data-label="Status">
          <StatusBadge v-bind="deploymentStatusDisplay(d.status)" :raw="d.status" />
        </td>
        <td data-label="Started" class="text-fg-muted">
          <TimeAgo :time="d.started_at" />
        </td>
        <td data-label="Duration" class="mono right text-fg-muted">
          {{ duration(d) }}
        </td>
        <td :data-label="d.error ? 'Error' : undefined" class="max-w-0 text-fg-muted max-sm:max-w-none" :class="d.error ? '' : 'max-sm:!hidden'">
          <span v-if="d.error" class="block truncate text-danger max-sm:whitespace-normal" :title="d.error">{{ d.error }}</span>
        </td>
      </tr>
    </tbody>
  </table>
</template>
