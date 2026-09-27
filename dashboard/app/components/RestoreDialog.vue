<script setup lang="ts">
import type { AgentEvent, SpecVolume } from '~/types/api'
import { agentUrl } from '~/composables/useAgent'
import { REQUEST_HEADERS } from '~/composables/useSession'
import { AgentError, errorFromResponse, toAgentError } from '~/utils/agentError'
import { formatBytes } from '~/utils/format'

/**
 * Replaces a volume's contents with an uploaded tar archive. The upload goes
 * through XMLHttpRequest rather than fetch for one reason: upload progress,
 * which a multi-gigabyte archive needs. Same proxy, same CSRF header, same
 * cookie session.
 */
const props = defineProps<{
  open: boolean
  application: string
  volume: SpecVolume | null
}>()

const emit = defineEmits<{
  close: []
  /** The agent answered 204: the volume holds the archive's contents and the application is still stopped. */
  restored: []
  /** The user chose to start the application from here. */
  start: []
}>()

const agent = useAgent()

type Phase = 'choose' | 'uploading' | 'done'
const phase = ref<Phase>('choose')
const file = ref<File | null>(null)
const progress = ref(0)
const error = shallowRef<AgentError | null>(null)
const restoredEvent = ref<AgentEvent | null>(null)
const picker = ref<HTMLInputElement | null>(null)

let request: XMLHttpRequest | null = null

watch(() => props.open, (open) => {
  if (!open) {
    request?.abort()
    request = null
    return
  }
  phase.value = 'choose'
  file.value = null
  progress.value = 0
  error.value = null
  restoredEvent.value = null
})

function onPick(event: Event) {
  const input = event.target as HTMLInputElement
  file.value = input.files?.[0] ?? null
  error.value = null
}

/** The first 512 bytes of a tar archive are a header with "ustar" at offset 257; refuse anything else before uploading. */
async function looksLikeTar(candidate: File): Promise<boolean> {
  if (candidate.size < 512) return false
  const head = new Uint8Array(await candidate.slice(257, 262).arrayBuffer())
  return String.fromCharCode(...head) === 'ustar'
}

async function submit() {
  const chosen = file.value
  if (!chosen || !props.volume || phase.value !== 'choose') return
  error.value = null
  if (!(await looksLikeTar(chosen))) {
    error.value = new AgentError(400, 'INVALID_REQUEST', `${chosen.name} is not a tar archive. Restore a backup downloaded from here or made with shipwick backup.`)
    return
  }

  phase.value = 'uploading'
  progress.value = 0
  const url = agentUrl(`/applications/${encodeURIComponent(props.application)}/volumes/${encodeURIComponent(props.volume.name)}/archive`)
  const xhr = new XMLHttpRequest()
  request = xhr
  xhr.open('PUT', url)
  for (const [name, value] of Object.entries(REQUEST_HEADERS)) xhr.setRequestHeader(name, value)
  xhr.setRequestHeader('Content-Type', 'application/x-tar')
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) progress.value = e.total > 0 ? e.loaded / e.total : 0
  }

  try {
    await new Promise<void>((resolve, reject) => {
      xhr.onload = () => resolve()
      xhr.onerror = () => reject(new AgentError(0, 'NETWORK', 'The upload failed before the agent answered. Check the connection and try again.'))
      xhr.onabort = () => reject(new AgentError(0, 'NETWORK', 'The upload was cancelled'))
      xhr.send(chosen)
    })
    if (xhr.status === 204) {
      phase.value = 'done'
      emit('restored')
      await loadRestoredEvent()
      return
    }
    // Any other status carries the agent's (or the proxy's) envelope.
    throw await errorFromResponse(new Response(xhr.responseText, { status: xhr.status }))
  }
  catch (cause) {
    error.value = toAgentError(cause)
    phase.value = 'choose'
  }
  finally {
    request = null
  }
}

/** The agent records what it restored; show its own sentence rather than a guess. */
async function loadRestoredEvent() {
  try {
    const events = await agent.get<AgentEvent[]>(`/applications/${encodeURIComponent(props.application)}/events`, { query: { limit: 5 } })
    restoredEvent.value = events.find(e => e.type === 'app' && /restored/i.test(e.message)) ?? null
  }
  catch {
    // The restore itself succeeded; the event is a nicety.
  }
}

const title = computed(() => (props.volume ? `Restore ${props.volume.name} of ${props.application}` : 'Restore from backup'))
</script>

<template>
  <UiDialog :open="props.open" :title="title" :busy="phase === 'uploading'" @close="emit('close')">
    <form v-if="phase !== 'done'" id="restore-form" class="space-y-4" @submit.prevent="submit">
      <div>
        <label for="restore-file" class="label block">Backup archive</label>
        <input
          id="restore-file"
          ref="picker"
          type="file"
          accept=".tar,application/x-tar"
          class="input mt-1.5 !h-auto cursor-pointer py-1 file:mr-3 file:cursor-pointer file:rounded-xs file:border-0 file:bg-active file:px-2 file:py-0.5 file:text-xs file:font-medium file:text-fg"
          :disabled="phase === 'uploading'"
          autofocus
          @change="onPick"
        >
        <p v-if="file" class="mono mt-1.5 text-xs text-fg-muted">
          {{ file.name }} · {{ formatBytes(file.size) }}
        </p>
      </div>
      <p v-if="props.volume" class="text-fg-muted">
        This replaces the data of volume <span class="mono text-fg">{{ props.volume.name }}</span> of <span class="mono text-fg">{{ props.application }}</span> with <span class="mono text-fg">{{ file?.name ?? 'the chosen file' }}</span>. The application must be stopped and is not started afterwards.
      </p>
      <div v-if="phase === 'uploading'" role="progressbar" :aria-valuenow="Math.round(progress * 100)" aria-valuemin="0" aria-valuemax="100" :aria-label="`Uploading ${file?.name ?? 'archive'}`">
        <div class="h-1.5 w-full overflow-hidden rounded-full bg-active">
          <div class="h-full rounded-full bg-fg transition-[width] duration-200" :style="{ width: `${Math.round(progress * 100)}%` }" />
        </div>
        <p class="mono mt-1.5 text-xs text-fg-muted">
          <template v-if="progress < 1">Uploading {{ Math.round(progress * 100) }}%</template>
          <template v-else>Uploaded. The agent is writing the volume…</template>
        </p>
      </div>
      <InlineError :error="error" />
    </form>

    <div v-else class="space-y-3">
      <p class="flex items-start gap-2 font-medium text-ok">
        <UiIcon name="check" :size="14" class="mt-[3px]" />
        <span>{{ restoredEvent?.message ?? `Volume ${props.volume?.name ?? ''} restored from ${file?.name ?? 'the backup'}` }}</span>
      </p>
      <p class="text-fg-muted">
        <span class="mono text-fg">{{ props.application }}</span> is still stopped. Start it when you are ready; it comes back with the restored data.
      </p>
    </div>

    <template #footer>
      <template v-if="phase !== 'done'">
        <UiButton :disabled="phase === 'uploading'" @click="emit('close')">
          Cancel
        </UiButton>
        <UiButton type="submit" form="restore-form" variant="danger-solid" :pending="phase === 'uploading'" :disabled="!file">
          Restore volume
        </UiButton>
      </template>
      <template v-else>
        <UiButton @click="emit('close')">
          Close
        </UiButton>
        <UiButton variant="primary" @click="emit('start')">
          <UiIcon name="play" :size="12" />
          Start application
        </UiButton>
      </template>
    </template>
  </UiDialog>
</template>
