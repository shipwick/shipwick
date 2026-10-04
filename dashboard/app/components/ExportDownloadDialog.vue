<script setup lang="ts">
import type { Application, ExportRequest } from '~/types/api'
import { REQUEST_HEADERS, currentAgentBase } from '~/composables/useSession'
import type { AgentError } from '~/utils/agentError'
import { AgentError as AgentFailure, errorFromResponse, toAgentError } from '~/utils/agentError'
import { exportBase, passphraseProblem, passphraseReady } from '~/utils/transfer'

/**
 * Everything the server runs, as one encrypted file saved by the browser.
 *
 * The passphrase is typed here and sent once, in the body of a request to the
 * dashboard's own server, which asks the agent and holds its answer; the
 * browser then fetches the file through a link and saves it as it arrives
 * (server/utils/downloads.ts). The passphrase is cleared from the page the
 * moment it is sent, is never part of an address and is stored nowhere.
 *
 * What the page knows afterwards is that the download began. The browser's
 * own list says how it goes; a file that broke off is reported there as
 * failed, and does not open.
 */
const props = defineProps<{
  open: boolean
  /** The applications on the server, to limit the export to some. */
  applications: readonly Application[]
}>()

const emit = defineEmits<{ close: [] }>()

const session = useSession()

const passphrase = ref('')
const again = ref('')
const chosen = ref<string[]>([])
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)
/** The file whose download began; "" until one has. */
const started = ref('')
const result = ref<HTMLElement | null>(null)
useFocusWhenShown(result)

watch(() => props.open, (open) => {
  // Nothing typed is kept once the dialog is closed.
  passphrase.value = ''
  again.value = ''
  if (!open) return
  chosen.value = []
  error.value = null
  started.value = ''
})

const problem = computed(() => passphraseProblem(passphrase.value, again.value))
const ready = computed(() => passphraseReady(passphrase.value, again.value))
// Only what has been deployed has anything to export.
const names = computed(() => props.applications.filter(a => a.version !== '' || a.static).map(a => a.name).sort())

function toggle(name: string) {
  chosen.value = chosen.value.includes(name) ? chosen.value.filter(n => n !== name) : [...chosen.value, name].sort()
}

async function start() {
  if (pending.value || !ready.value) return
  pending.value = true
  error.value = null
  const body: ExportRequest = { passphrase: passphrase.value, ...(chosen.value.length > 0 ? { applications: chosen.value } : {}) }
  // Gone from the page before the answer, whatever the answer is.
  passphrase.value = ''
  again.value = ''
  const base = exportBase(currentAgentBase())
  try {
    let response: Response
    try {
      response = await fetch(base, {
        method: 'POST',
        headers: { ...REQUEST_HEADERS, 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        credentials: 'same-origin',
        cache: 'no-store',
      })
    }
    catch {
      throw new AgentFailure(0, 'NETWORK', 'Cannot reach the dashboard server')
    }
    if (response.status === 401) {
      const refusal = await errorFromResponse(response)
      session.expire(refusal)
      throw refusal
    }
    if (!response.ok) throw await errorFromResponse(response)
    const answer = await response.json() as { data?: { ticket?: unknown, filename?: unknown } }
    const ticket = typeof answer.data?.ticket === 'string' ? answer.data.ticket : ''
    if (!/^[\w-]{16,128}$/.test(ticket)) throw new AgentFailure(502, 'BAD_RESPONSE', 'The dashboard server did not say where to fetch the export')
    // A link the browser follows itself: the file goes to disk as it arrives, and never through this page's memory.
    const link = document.createElement('a')
    link.href = `${base}/${ticket}`
    link.download = ''
    link.rel = 'noopener'
    document.body.append(link)
    link.click()
    link.remove()
    started.value = typeof answer.data?.filename === 'string' && answer.data.filename !== '' ? answer.data.filename : 'the export'
  }
  catch (cause) {
    error.value = toAgentError(cause)
  }
  finally {
    pending.value = false
  }
}

const CHIP = 'target inline-flex h-6 items-center justify-center gap-1 rounded-sm border px-2 text-xs transition-colors duration-100'
</script>

<template>
  <UiDialog :open="props.open" title="Download an export" size="md" :busy="pending" @close="emit('close')">
    <div v-if="started" ref="result" tabindex="-1" class="space-y-3 outline-none">
      <p class="flex items-start gap-2 font-medium text-ok" role="status">
        <UiIcon name="check" :size="14" class="mt-[3px]" />
        <span class="min-w-0">The download of <span class="mono break-all">{{ started }}</span> has begun.</span>
      </p>
      <p class="text-fg-muted">
        The file is written by the server as it is sent, so its size is not known beforehand and the browser shows no percentage. The browser's list of downloads says when it is done.
      </p>
      <p class="text-fg-muted">
        This page cannot see how the download ends. If the export breaks off on the server, the browser reports the download as failed: start it again. A file the browser lists as complete arrived whole; it opens only with the passphrase, which is kept nowhere.
      </p>
      <p class="text-xs text-fg-subtle">
        To move to another server, sign in to that server and import the file under Export and standby, or run <span class="mono text-fg-muted">shipwick import</span> there.
      </p>
    </div>

    <form v-else id="export-download-form" class="space-y-5" @submit.prevent="start">
      <p class="text-fg-muted">
        One file with everything another server needs to run what this one runs: every application's configuration with its values, the stored secrets, registry credentials and certificates, images built by the CLI, static folders and every volume. It exists only encrypted.
      </p>

      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <label for="export-passphrase" class="label block">Passphrase</label>
          <input
            id="export-passphrase"
            v-model="passphrase"
            type="password"
            class="input mono mt-1.5"
            autocomplete="new-password"
            autocapitalize="off"
            spellcheck="false"
            autofocus
            :aria-invalid="problem !== '' || undefined"
            aria-describedby="export-passphrase-help"
          >
        </div>
        <div>
          <label for="export-passphrase-again" class="label block">Once more</label>
          <input
            id="export-passphrase-again"
            v-model="again"
            type="password"
            class="input mono mt-1.5"
            autocomplete="new-password"
            autocapitalize="off"
            spellcheck="false"
            :aria-invalid="problem !== '' || undefined"
            aria-describedby="export-passphrase-help"
          >
        </div>
        <p id="export-passphrase-help" class="text-xs sm:col-span-2" :class="problem ? 'text-danger' : 'text-fg-muted'">
          {{ problem || 'At least 12 characters. It encrypts the file and is the only way to open it again: neither the server nor the dashboard keeps it.' }}
        </p>
      </div>

      <fieldset v-if="names.length > 1" class="min-w-0">
        <legend class="label">
          Applications
        </legend>
        <div class="mt-1.5 flex flex-wrap gap-1.5">
          <button
            v-for="name in names"
            :key="name"
            type="button"
            class="mono"
            :class="[CHIP, chosen.includes(name) ? 'border-fg bg-active font-medium text-fg' : 'border-line-strong text-fg-muted hover:bg-hover hover:text-fg']"
            :aria-pressed="chosen.includes(name)"
            @click="toggle(name)"
          >
            <UiIcon v-if="chosen.includes(name)" name="check" :size="10" />
            {{ name }}
          </button>
        </div>
        <p class="mt-1.5 text-xs text-fg-muted">
          <template v-if="chosen.length === 0">
            None chosen: every application.
          </template>
          <template v-else>
            Only {{ chosen.length === 1 ? 'this one' : `these ${chosen.length}` }}. The secrets, registry credentials and certificates come along either way.
          </template>
        </p>
      </fieldset>

      <p class="text-xs text-fg-subtle">
        Each application is held for the moment its volumes are read, as a backup holds it. The export is written to the audit trail.
      </p>

      <InlineError :error="error" />
    </form>

    <template #footer>
      <template v-if="started">
        <UiButton variant="primary" @click="emit('close')">
          Close
        </UiButton>
      </template>
      <template v-else>
        <UiButton :disabled="pending" @click="emit('close')">
          Cancel
        </UiButton>
        <UiButton type="submit" form="export-download-form" variant="primary" :pending="pending" :disabled="!ready">
          <UiIcon name="download" :size="12" />
          Download
        </UiButton>
      </template>
    </template>
  </UiDialog>
</template>
