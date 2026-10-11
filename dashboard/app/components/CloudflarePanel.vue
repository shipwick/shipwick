<script setup lang="ts">
import type { CloudflareStatus } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { cloudflareDisplay, cloudflareSummary, verifyRefusal, zoneDisplay } from '~/utils/cloudflare'
import { formatAbsoluteUtc } from '~/utils/format'

/**
 * What the agent found out about its Cloudflare token: whether it is valid
 * from the server's own address, which zones it sees and in which it could
 * write a DNS record, and each problem as the sentence the agent wrote. An
 * admin may ask for a fresh check; the agent takes one a minute, and says
 * when the next may come. Nothing of the token is in the answer, and nothing
 * of it is shown.
 */
const props = defineProps<{
  status: CloudflareStatus
  mayAdmin: boolean
}>()
const emit = defineEmits<{ checked: [status: CloudflareStatus] }>()

const agent = useAgent()

/** The result of a check asked from here, shown until the server's polling catches up with it. */
const fresh = shallowRef<CloudflareStatus | null>(null)
const shown = computed(() => (fresh.value && fresh.value.checked_at >= props.status.checked_at ? fresh.value : props.status))

const display = computed(() => cloudflareDisplay(shown.value))
const summary = computed(() => cloudflareSummary(shown.value))

const checking = ref(false)
const checkError = shallowRef<AgentError | null>(null)
/** What a refusal said, next to the button; gone with the next successful check. */
const refused = ref('')
/** The agent has no token to check, or is older than the endpoint: the button goes for good. */
const hidden = ref(false)
/** Seconds until the agent takes another check, counted down. */
const waitSeconds = ref(0)
let waitTimer: ReturnType<typeof setInterval> | null = null

function waitFor(seconds: number) {
  if (waitTimer !== null) clearInterval(waitTimer)
  waitSeconds.value = seconds
  if (seconds <= 0) return
  waitTimer = setInterval(() => {
    waitSeconds.value -= 1
    if (waitSeconds.value <= 0 && waitTimer !== null) {
      clearInterval(waitTimer)
      waitTimer = null
      refused.value = ''
    }
  }, 1000)
}

async function check() {
  if (checking.value || waitSeconds.value > 0) return
  checking.value = true
  checkError.value = null
  refused.value = ''
  try {
    const result = await agent.post<CloudflareStatus>('/server/cloudflare/verify')
    fresh.value = result
    emit('checked', result)
  }
  catch (cause) {
    const error = toAgentError(cause)
    const refusal = verifyRefusal(error)
    if (refusal.hide) hidden.value = true
    if (refusal.message) {
      refused.value = refusal.message
      waitFor(refusal.retryAfterSeconds)
    }
    else {
      checkError.value = error
    }
  }
  finally {
    checking.value = false
  }
}

onScopeDispose(() => {
  if (waitTimer !== null) clearInterval(waitTimer)
})
</script>

<template>
  <UiPanel title="Cloudflare">
    <template v-if="props.mayAdmin && !hidden" #actions>
      <UiButton
        size="sm"
        :pending="checking"
        :disabled="waitSeconds > 0"
        :hint="waitSeconds > 0 ? `The agent takes one check a minute; the next in ${waitSeconds}s` : 'Ask Cloudflare again, from this server: the token, its zones, and a record written and removed in each'"
        @click="check"
      >
        <UiIcon name="refresh" :size="12" />
        Check again
      </UiButton>
    </template>
    <div class="space-y-3 px-4 py-3">
      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <StatusBadge :tone="display.tone" :label="display.label" />
        <span class="text-fg-muted">{{ summary }}</span>
        <span class="text-xs text-fg-subtle">
          checked <TimeAgo :time="shown.checked_at" /><span class="mono ml-2">{{ formatAbsoluteUtc(shown.checked_at) }}</span>
        </span>
      </div>

      <!-- Each problem is a sentence the agent wrote to be acted on; the cause first, then the remedy. -->
      <ul v-if="shown.problems.length > 0" class="space-y-1.5" aria-label="What is wrong with the token">
        <li v-for="problem in shown.problems" :key="problem" class="flex items-start gap-2 rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger">
          <UiIcon name="x-circle" :size="14" class="mt-px shrink-0" />
          <span class="min-w-0 break-words">{{ problem }}</span>
        </li>
      </ul>

      <dl class="facts sm:grid-cols-2">
        <div>
          <dt class="label">
            Server's address, as Cloudflare saw it
          </dt>
          <dd class="mt-0.5">
            <span v-if="shown.address" class="mono break-all">{{ shown.address }}</span>
            <span v-else class="text-fg-muted">Not learned: Cloudflare's API did not say where the check came from.</span>
            <span v-if="shown.address" class="block text-xs text-fg-muted">An IP filter on the token has to name this address.</span>
          </dd>
        </div>
        <div>
          <dt class="label">
            Zones the token sees
          </dt>
          <dd class="mt-0.5">
            <ul v-if="shown.zones.length > 0" class="space-y-0.5" aria-label="Zones">
              <li v-for="zone in shown.zones" :key="zone.name" class="flex flex-wrap items-center gap-x-3">
                <span class="mono break-all">{{ zone.name }}</span>
                <StatusBadge v-bind="zoneDisplay(zone)" :detail="zone.dns_edit ? 'The agent created and removed a TXT record of its own in this zone' : 'The agent could not write a record in this zone: the token needs Zone → DNS → Edit on it'" />
              </li>
            </ul>
            <span v-else class="text-fg-muted">None: the token needs Zone → Zone → Read on the zone of every hostname.</span>
          </dd>
        </div>
      </dl>

      <p v-if="refused" class="text-xs text-fg-muted" role="status">
        {{ refused }}
      </p>
      <InlineError :error="checkError" />
    </div>
  </UiPanel>
</template>
