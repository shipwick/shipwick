<script setup lang="ts">
import type { Alert } from '~/types/api'
import { alertKindLabel, alertSubject, alertTone, sortAlerts } from '~/utils/alerts'

/**
 * The conditions the agent says hold right now, worst first. The message is
 * the agent's own sentence and already says what to do; this only frames it.
 */
const props = withDefaults(defineProps<{
  alerts: Alert[]
  /** Link the application an alert is about; off on that application's own page. */
  linkApplication?: boolean
}>(), { linkApplication: true })

const sorted = computed(() => sortAlerts(props.alerts))

const FRAME = { warn: 'border-warn-line bg-warn-bg text-warn', danger: 'border-danger-line bg-danger-bg text-danger', ok: '', muted: '' } as const
</script>

<template>
  <ul v-if="sorted.length > 0" class="space-y-2" aria-label="Active alerts">
    <li
      v-for="alert in sorted"
      :key="`${alert.kind}/${alert.application}/${alert.replica}`"
      class="flex items-start gap-2 rounded-sm border px-3 py-2 text-xs"
      :class="FRAME[alertTone(alert)]"
      role="status"
    >
      <UiIcon :name="alert.severity === 'critical' ? 'x-circle' : 'alert'" :size="14" class="mt-px" />
      <div class="min-w-0 flex-1">
        <p class="flex flex-wrap items-baseline gap-x-2">
          <span class="font-medium">{{ alert.severity === 'critical' ? 'Critical' : 'Warning' }} · {{ alertKindLabel(alert) }}</span>
          <NuxtLink v-if="props.linkApplication && alert.application" :to="`/applications/${alert.application}`" class="mono underline underline-offset-2">{{ alertSubject(alert) }}</NuxtLink>
          <span v-else class="mono">{{ alertSubject(alert) }}</span>
          <span class="opacity-80">since <TimeAgo :time="alert.since" /></span>
        </p>
        <p class="mt-0.5 break-words">
          {{ alert.message }}
        </p>
      </div>
    </li>
  </ul>
</template>
