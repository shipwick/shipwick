<script setup lang="ts">
import type { RequestLine } from '~/types/api'
import { formatBytes, formatLogTime } from '~/utils/format'
import { formatLatency, newestFirst, statusTone } from '~/utils/traffic'

/**
 * The application's most recent requests as the proxy logged them. There is
 * no stream and no cursor: the agent keeps the last 200 in memory and the
 * list is asked for again every few seconds, while this is on screen and the
 * tab is visible.
 */
const props = defineProps<{ application: string }>()

/** All the agent keeps per application. */
const TAIL = 200
const REFRESH_INTERVAL_MS = 3000

const agent = useAgent()
const requests = usePolling<RequestLine[]>(signal => agent.get<RequestLine[]>(
  `/applications/${encodeURIComponent(props.application)}/requests`,
  { query: { tail: TAIL }, signal },
), { interval: REFRESH_INTERVAL_MS })

watch(() => props.application, () => void requests.reset())

const rows = computed(() => newestFirst(requests.data.value ?? []))

const TONE_TEXT = { ok: 'text-ok', warn: 'text-warn', danger: 'text-danger', muted: 'text-fg-muted' } as const
</script>

<template>
  <div>
    <TableSkeleton v-if="requests.loading.value && !requests.data.value" :rows="4" :columns="6" />
    <ErrorState
      v-else-if="requests.error.value && !requests.data.value"
      :error="requests.error.value"
      subject="the requests"
      :retrying="requests.refreshing.value"
      @retry="requests.refresh()"
    />
    <EmptyState v-else-if="rows.length === 0" title="No requests yet">
      The agent keeps the last {{ TAIL }} requests in memory, so the list starts empty when the agent starts.
    </EmptyState>
    <template v-else>
      <div class="max-h-96 overflow-auto">
        <table class="data-table stack" aria-label="Recent requests">
          <thead>
            <tr>
              <th class="sticky top-0">
                Time <span class="normal-case tracking-normal text-fg-subtle">UTC</span>
              </th>
              <th class="sticky top-0">
                Status
              </th>
              <th class="sticky top-0">
                Request
              </th>
              <th class="right sticky top-0">
                Duration
              </th>
              <th class="right sticky top-0">
                Size
              </th>
              <th class="sticky top-0">
                Client
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(r, index) in rows" :key="`${r.time}-${index}`">
              <td data-label="Time" class="mono whitespace-nowrap text-fg-muted" :title="r.time">
                {{ formatLogTime(r.time) }}
              </td>
              <td data-label="Status" class="mono font-medium" :class="TONE_TEXT[statusTone(r.status)]">
                {{ r.status }}
              </td>
              <td data-primary class="mono cards:order-first">
                <span class="text-fg-muted">{{ r.method }}</span> <span class="break-all">{{ r.path }}</span>
              </td>
              <td data-label="Duration" class="mono right whitespace-nowrap text-fg-muted">
                {{ formatLatency(r.duration_ms) }}
              </td>
              <td data-label="Size" class="mono right whitespace-nowrap text-fg-muted">
                {{ formatBytes(r.bytes) }}
              </td>
              <td data-label="Client" class="mono text-fg-muted">
                {{ r.client }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
        The last {{ TAIL }} requests since the agent started, newest first, as the proxy logged them: paths without their query string, no headers. Refreshed every few seconds.
      </p>
    </template>
  </div>
</template>
