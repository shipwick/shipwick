<script setup lang="ts">
import type { Promotion } from '~/types/api'
import { promotionAnnouncement } from '~/utils/announce'
import { promotedDisplay, promotionOutcome, promotionProgress, promotionRunning, recordValue } from '~/utils/transfer'

/**
 * A promotion as it runs and after it ended: a row per application with its
 * state and the agent's message, and the DNS records that send visitors here,
 * which the agent names from its first answer on.
 */
const props = defineProps<{
  promotion: Promotion
  /** The agent stopped answering while the promotion ran; it is asked again, and the promotion goes on without us. */
  away?: boolean
}>()

const emit = defineEmits<{ dismiss: [] }>()

const running = computed(() => promotionRunning(props.promotion))

// Each application whose state moved is said once, and the end. The panel is
// polled and shows a clock: live as a whole, it would be read again and again.
const { announce } = useAnnounce()
watch(() => props.promotion, (next, previous) => announce(promotionAnnouncement(previous ?? null, next)), { immediate: true })
</script>

<template>
  <section
    class="overflow-hidden rounded-sm border"
    :class="running ? 'border-warn-line' : props.promotion.status === 'failed' ? 'border-danger-line' : 'border-line'"
    aria-label="Promotion"
  >
    <header class="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-b border-line bg-subtle px-4 py-2.5">
      <p class="flex min-w-0 items-center gap-2 font-medium">
        <svg v-if="running" class="spinner shrink-0 text-warn" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" aria-hidden="true">
          <path d="M8 2a6 6 0 1 1-6 6" />
        </svg>
        <UiIcon v-else :name="props.promotion.status === 'failed' ? 'x-circle' : 'check'" :size="14" :class="props.promotion.status === 'failed' ? 'text-danger' : 'text-ok'" />
        <span class="min-w-0">{{ running ? promotionProgress(props.promotion) : 'This server was promoted' }}</span>
      </p>
      <p class="flex items-center gap-3 text-xs text-fg-muted">
        <span v-if="running">started <TimeAgo :time="props.promotion.started_at" /></span>
        <span v-else><TimeAgo :time="props.promotion.completed_at" /> · {{ promotionOutcome(props.promotion) }}</span>
        <UiButton v-if="!running" size="sm" variant="ghost" @click="emit('dismiss')">
          Dismiss
        </UiButton>
      </p>
    </header>

    <p v-if="props.away && running" class="flex items-start gap-2 border-b border-line px-4 py-2 text-xs text-warn" role="status">
      <UiIcon name="alert" :size="12" class="mt-[3px]" />
      The agent is not answering right now. The promotion goes on without this page, and is picked up again by an agent that restarts; asking again.
    </p>

    <ul class="divide-y divide-line">
      <li v-for="a in props.promotion.applications" :key="a.name" class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 px-4 py-2">
        <NuxtLink :to="`/applications/${a.name}`" class="mono font-medium hover:underline">{{ a.name }}</NuxtLink>
        <StatusBadge v-bind="promotedDisplay(a)" :raw="a.status" />
        <span v-if="a.message" class="basis-full break-words text-xs" :class="a.status === 'failed' ? 'text-danger' : 'text-fg-muted'">{{ a.message }}</span>
      </li>
    </ul>

    <div v-if="props.promotion.records.length > 0" class="border-t border-line px-4 py-3">
      <p class="label mb-1.5">
        DNS records to point here
      </p>
      <div class="overflow-x-auto rounded-sm border border-line">
        <table class="data-table !text-xs" aria-label="DNS records to point here">
          <thead>
            <tr>
              <th>Hostname</th>
              <th>Type</th>
              <th>Value</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="record in props.promotion.records" :key="`${record.hostname}/${record.type}`">
              <td class="mono break-all">
                {{ record.hostname }}
              </td>
              <td class="mono">
                {{ record.type }}
              </td>
              <td class="mono break-all" :class="record.value ? '' : 'font-sans text-fg-muted'">
                {{ recordValue(record) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="mt-1.5 text-xs text-fg-muted">
        Until a record points here its hostname still reaches the old server. Certificates are obtained once it does.
      </p>
    </div>
  </section>
</template>
