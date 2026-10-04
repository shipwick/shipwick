<script setup lang="ts">
import type { Import } from '~/types/api'
import { agentUrl } from '~/composables/useAgent'
import { REQUEST_HEADERS } from '~/composables/useSession'
import { AgentError, errorFromResponse, toAgentError } from '~/utils/agentError'
import { formatBytes } from '~/utils/format'
import { OVERWRITE_WORD, encodePassphrase, importOutcome, importProgress, importedDisplay, looksLikeExport } from '~/utils/transfer'

/**
 * Deploys what an export file holds on this server. The file goes from the
 * browser through the dashboard's proxy to the agent as a stream: nobody
 * holds it whole, and the agent imports it while it arrives, so the upload
 * and the import are one thing and end together. XMLHttpRequest rather than
 * fetch, for the upload's progress.
 *
 * The passphrase travels in a header of that one request, base64-encoded as
 * the agent expects it; it is cleared from the page when the request is
 * sent, is never part of an address and is stored nowhere.
 */
const props = defineProps<{ open: boolean }>()

const emit = defineEmits<{
  close: []
  /** The upload has begun: the agent has an import to follow. */
  started: []
  /** The agent answered with the import as it ended. */
  done: [run: Import]
}>()

const agent = useAgent()
const session = useSession()

type Phase = 'choose' | 'uploading' | 'done'
const phase = ref<Phase>('choose')
const file = ref<File | null>(null)
const passphrase = ref('')
const stopped = ref(false)
const overwrite = ref(false)
const confirmation = ref('')
const progress = ref(0)
const error = shallowRef<AgentError | null>(null)
const outcome = shallowRef<Import | null>(null)
const result = ref<HTMLElement | null>(null)
useFocusWhenShown(result)

let request: XMLHttpRequest | null = null

watch(() => props.open, (open) => {
  passphrase.value = ''
  if (!open) return
  phase.value = 'choose'
  file.value = null
  stopped.value = false
  overwrite.value = false
  confirmation.value = ''
  progress.value = 0
  error.value = null
  outcome.value = null
})

function onPick(event: Event) {
  file.value = (event.target as HTMLInputElement).files?.[0] ?? null
  error.value = null
}

const confirmed = computed(() => !overwrite.value || confirmation.value.trim().toLowerCase() === OVERWRITE_WORD)
const ready = computed(() => file.value !== null && passphrase.value !== '' && confirmed.value)

// While the file arrives the agent says how far the import is: asked on a second connection, as `shipwick import` does.
const following = usePolling<Import | null>(async (signal) => {
  try {
    return await agent.get<Import>('/import', { signal })
  }
  catch (cause) {
    if (cause instanceof AgentError && cause.code === 'NOT_FOUND') return null
    throw cause
  }
}, { interval: 1500, enabled: () => props.open && phase.value === 'uploading' })
const running = computed(() => (phase.value === 'uploading' && following.data.value?.status === 'running' && following.data.value.source === 'upload' ? following.data.value : null))

async function submit() {
  const chosen = file.value
  if (!chosen || !ready.value || phase.value !== 'choose') return
  error.value = null
  if (!looksLikeExport(new Uint8Array(await chosen.slice(0, 8).arrayBuffer()))) {
    error.value = new AgentError(400, 'INVALID_EXPORT', `${chosen.name} is not an export: it does not start like a file Shipwick writes. An export is downloaded here, or written by shipwick export.`)
    return
  }

  const xhr = new XMLHttpRequest()
  request = xhr
  xhr.open('POST', agentUrl('/import', { stopped: stopped.value, overwrite: overwrite.value }))
  for (const [name, value] of Object.entries(REQUEST_HEADERS)) xhr.setRequestHeader(name, value)
  xhr.setRequestHeader('Content-Type', 'application/octet-stream')
  xhr.setRequestHeader('X-Shipwick-Passphrase', encodePassphrase(passphrase.value))
  // Gone from the page once it is on its way, whatever the answer is.
  passphrase.value = ''
  xhr.upload.onprogress = (e) => {
    if (e.lengthComputable) progress.value = e.total > 0 ? e.loaded / e.total : 0
  }

  phase.value = 'uploading'
  progress.value = 0
  emit('started')
  try {
    await new Promise<void>((resolve, reject) => {
      xhr.onload = () => resolve()
      xhr.onerror = () => reject(new AgentError(0, 'NETWORK', 'The upload broke off before the server answered. What had been imported by then is in place; the list of applications says what that is. Import again with "Replace what exists" for the rest.'))
      xhr.onabort = () => reject(new AgentError(0, 'NETWORK', 'The upload was cancelled. What had been imported by then is in place.'))
      xhr.send(chosen)
    })
    if (xhr.status === 200) {
      const run = (JSON.parse(xhr.responseText) as { data: Import }).data
      outcome.value = run
      phase.value = 'done'
      emit('done', run)
      return
    }
    // Any other status carries the agent's (or the proxy's) envelope.
    const refusal = await errorFromResponse(new Response(xhr.responseText, { status: xhr.status }))
    if (xhr.status === 401) session.expire(refusal)
    throw refusal
  }
  catch (cause) {
    error.value = toAgentError(cause)
    phase.value = 'choose'
  }
  finally {
    request = null
  }
}

function cancel() {
  if (phase.value === 'uploading') request?.abort()
  else emit('close')
}

onScopeDispose(() => request?.abort())
</script>

<template>
  <UiDialog :open="props.open" title="Import a file" size="md" :busy="phase === 'uploading'" @close="emit('close')">
    <div v-if="phase === 'done' && outcome" ref="result" tabindex="-1" class="space-y-3 outline-none">
      <p class="flex items-start gap-2 font-medium" :class="outcome.status === 'failed' ? 'text-danger' : 'text-ok'" role="status">
        <UiIcon :name="outcome.status === 'failed' ? 'x-circle' : 'check'" :size="14" class="mt-[3px]" />
        <span class="min-w-0">The import is over: {{ importOutcome(outcome) }}.</span>
      </p>
      <p v-if="outcome.error" class="break-words text-danger">
        {{ outcome.error }}
      </p>
      <p class="text-fg-muted">
        Every application's outcome, and what was kept as it was, is under <span class="text-fg">Last import</span> on this page.
      </p>
    </div>

    <form v-else id="import-file-form" class="space-y-5" @submit.prevent="submit">
      <div>
        <label for="import-file" class="label block">Export file</label>
        <input
          id="import-file"
          type="file"
          accept=".swexport,application/octet-stream"
          class="input mt-1.5 !h-auto cursor-pointer py-1 file:mr-3 file:cursor-pointer file:rounded-xs file:border-0 file:bg-active file:px-2 file:py-0.5 file:text-xs file:font-medium file:text-fg"
          :disabled="phase === 'uploading'"
          autofocus
          @change="onPick"
        >
        <p class="mono mt-1.5 text-xs text-fg-muted">
          <template v-if="file">
            {{ file.name }} · {{ formatBytes(file.size) }}
          </template>
          <span v-else class="font-sans">A <span class="mono">.swexport</span> file: downloaded from a server's dashboard, or written by <span class="mono">shipwick export</span>.</span>
        </p>
      </div>

      <div v-if="phase === 'choose'">
        <label for="import-passphrase" class="label block">Its passphrase</label>
        <input
          id="import-passphrase"
          v-model="passphrase"
          type="password"
          class="input mono mt-1.5"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          aria-describedby="import-passphrase-help"
        >
        <p id="import-passphrase-help" class="mt-1.5 text-xs text-fg-muted">
          The one the export was written with. It is sent with the file and kept nowhere.
        </p>
      </div>

      <fieldset v-if="phase === 'choose'" class="min-w-0 space-y-3">
        <legend class="label">
          How
        </legend>
        <label class="target flex cursor-pointer items-start gap-2.5">
          <input v-model="stopped" type="checkbox" class="mt-1 accent-[var(--fg)]" aria-describedby="import-stopped-help">
          <span class="min-w-0">
            <span class="font-medium">Deploy the applications without starting them</span>
            <span id="import-stopped-help" class="block text-xs text-fg-muted">For a server that stands by: everything is in place and nothing runs until it is promoted. An application that is running here is left alone.</span>
          </span>
        </label>
        <label class="target flex cursor-pointer items-start gap-2.5">
          <input v-model="overwrite" type="checkbox" class="mt-1 accent-[var(--fg)]" aria-describedby="import-overwrite-help">
          <span class="min-w-0">
            <span class="font-medium">Replace what exists under the same name</span>
            <span id="import-overwrite-help" class="block text-xs text-fg-muted">Without this, an application, a secret, a registry credential or a certificate that exists here is skipped and named. With it they are replaced, and an application loses the data in its volumes to what the file holds.</span>
          </span>
        </label>
        <div v-if="overwrite" class="rounded-sm border border-danger-line bg-danger-bg px-3 py-2.5">
          <label for="import-confirm" class="block text-xs text-danger">
            This deletes data on this server that the file replaces. Type <span class="mono select-all font-medium">{{ OVERWRITE_WORD }}</span> to confirm
          </label>
          <input id="import-confirm" v-model="confirmation" type="text" class="input mono mt-1.5" autocomplete="off" autocapitalize="off" spellcheck="false">
        </div>
      </fieldset>

      <div v-if="phase === 'uploading'" class="space-y-3">
        <div role="progressbar" :aria-valuenow="Math.round(progress * 100)" aria-valuemin="0" aria-valuemax="100" :aria-label="`Uploading ${file?.name ?? 'the export'}`">
          <div class="h-1.5 w-full overflow-hidden rounded-full bg-active">
            <div class="h-full rounded-full bg-fg transition-[width] duration-200" :style="{ width: `${Math.round(progress * 100)}%` }" />
          </div>
          <p class="mono mt-1.5 text-xs text-fg-muted">
            <template v-if="progress < 1">
              Sent {{ Math.round(progress * 100) }}% of {{ formatBytes(file?.size) }}
            </template>
            <template v-else>
              All of it is sent. The server is finishing the last applications…
            </template>
          </p>
        </div>
        <p class="font-medium">
          {{ running ? importProgress(running) : 'The server is reading the export…' }}
        </p>
        <ul v-if="running && running.applications.length > 0" class="divide-y divide-line rounded-sm border border-line" aria-label="Applications of the import">
          <li v-for="a in running.applications" :key="a.name" class="flex flex-wrap items-center justify-between gap-x-3 gap-y-0.5 px-3 py-1.5">
            <span class="mono min-w-0 break-all">{{ a.name }}</span>
            <StatusBadge v-bind="importedDisplay(a)" :raw="a.status" />
          </li>
        </ul>
        <p class="text-xs text-fg-muted">
          Keep this page open. The server deploys each application as its part of the file arrives, so the upload pauses while one is deployed, and closing the page ends the import where it is.
        </p>
      </div>

      <InlineError :error="error" />
    </form>

    <template #footer>
      <template v-if="phase === 'done'">
        <UiButton variant="primary" @click="emit('close')">
          Close
        </UiButton>
      </template>
      <template v-else>
        <UiButton @click="cancel">
          {{ phase === 'uploading' ? 'Cancel the import' : 'Cancel' }}
        </UiButton>
        <UiButton type="submit" form="import-file-form" :variant="overwrite ? 'danger-solid' : 'primary'" :pending="phase === 'uploading'" :disabled="!ready">
          <UiIcon name="upload" :size="12" />
          Import
        </UiButton>
      </template>
    </template>
  </UiDialog>
</template>
