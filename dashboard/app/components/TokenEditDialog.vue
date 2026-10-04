<script setup lang="ts">
import type { Token } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import type { EditExpiryChoice } from '~/utils/access'
import { editExpiryChoices, expiryFromChoice, listNames, tokenChanges, tokenExpiryDisplay } from '~/utils/access'
import { formatAbsoluteUtc } from '~/utils/format'
import { ROLE_DESCRIPTIONS } from '~/utils/roles'

/**
 * Changes a token without changing its value: the applications a deploy
 * token is limited to, and its end. Whatever uses the token goes on using
 * it. The role, the name and the value never change; only what was changed
 * here is sent.
 */
const props = defineProps<{
  token: Token | null
  /** The applications on the server, to choose from. */
  known: string[]
}>()

const emit = defineEmits<{
  close: []
  /** The agent stored the change: the token as it is now. */
  changed: [token: Token]
}>()

const agent = useAgent()
const now = useNow()

const applications = ref<string[]>([])
const expiry = ref<EditExpiryChoice>('keep')
const date = ref('')
const pending = ref(false)
const error = shallowRef<AgentError | null>(null)

watch(() => props.token, (token) => {
  if (!token) return
  applications.value = [...(token.applications ?? [])]
  // An expired token is opened to be given a new end: the common thing first.
  expiry.value = tokenExpiryDisplay(token.expires_at, now.value)?.expired ? '30' : 'keep'
  date.value = ''
  error.value = null
})

const t = computed(() => props.token)
const display = computed(() => tokenExpiryDisplay(t.value?.expires_at, now.value))
const choices = computed(() => (t.value ? editExpiryChoices(t.value, now.value) : []))
const changes = computed(() => (t.value ? tokenChanges(t.value, { applications: applications.value, expiry: expiry.value, date: date.value }, now.value) : { body: null, problem: '' }))
const newEnd = computed(() => (expiry.value === 'keep' || expiry.value === 'never' ? null : expiryFromChoice(expiry.value, date.value, now.value)))
const today = computed(() => new Date(now.value).toISOString().slice(0, 10))

async function save() {
  const token = t.value
  const body = changes.value.body
  if (!token || !body || pending.value) return
  pending.value = true
  error.value = null
  try {
    emit('changed', await agent.put<Token>(`/tokens/${encodeURIComponent(token.name)}`, { body }))
  }
  catch (cause) {
    error.value = toAgentError(cause)
  }
  finally {
    pending.value = false
  }
}

/** An agent before 0.7 knows the address for revoking only: changing a token is an operation it lacks. */
const tooOld = computed(() => error.value?.code === 'ENDPOINT_NOT_FOUND')
</script>

<template>
  <UiDialog :open="t !== null" :title="`Change ${t?.name ?? ''}`" size="md" :busy="pending" @close="emit('close')">
    <form v-if="t" id="token-edit-form" class="space-y-5" @submit.prevent="save">
      <p v-if="display?.expired" class="flex items-start gap-2 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn">
        <UiIcon name="alert" :size="14" class="mt-px" />
        <span class="min-w-0">This token {{ display.label }} and is refused. With a new end it works again, with the same value: nothing that uses it has to change.</span>
      </p>

      <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-sm">
        <dt class="label pt-0.5">
          Role
        </dt>
        <dd>
          <span class="mono">{{ t.role }}</span>
          <span class="text-fg-muted"> · {{ ROLE_DESCRIPTIONS[t.role] }}</span>
          <span class="block text-xs text-fg-subtle">The role cannot be changed: create another token with the role that is needed, and revoke this one.</span>
        </dd>
      </dl>

      <ApplicationsPicker
        id="token-edit-applications"
        v-model="applications"
        :known="props.known"
        :disabled="t.role !== 'deploy'"
        :disabled-reason="t.role === 'read' ? 'A read token changes nothing, so there is nothing to limit.' : 'The admin role is for the whole server; only a deploy token can be limited to applications.'"
      />

      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <label for="token-edit-expiry" class="label block">End</label>
          <select id="token-edit-expiry" v-model="expiry" class="input mt-1.5" aria-describedby="token-edit-expiry-help" autofocus>
            <option v-for="choice in choices" :key="choice.value" :value="choice.value">
              {{ choice.label }}
            </option>
          </select>
        </div>
        <div v-if="expiry === 'date'">
          <label for="token-edit-date" class="label block">The last day it works</label>
          <input id="token-edit-date" v-model="date" type="date" class="input mono mt-1.5" :min="today" :aria-invalid="(date !== '' && changes.problem !== '') || undefined" aria-describedby="token-edit-expiry-help">
        </div>
        <p id="token-edit-expiry-help" class="text-xs sm:col-span-2" :class="expiry === 'date' && date !== '' && changes.problem ? 'text-danger' : 'text-fg-muted'">
          <template v-if="expiry === 'keep'">
            <template v-if="t.expires_at">
              {{ display?.expired ? 'Expired' : 'Ends' }} <span class="mono">{{ formatAbsoluteUtc(t.expires_at) }}</span>.
            </template>
            <template v-else>
              Works until it is revoked.
            </template>
          </template>
          <template v-else-if="expiry === 'never'">
            Works until it is revoked.
          </template>
          <template v-else-if="newEnd?.problem">
            {{ date === '' ? 'Works until the end of that day, UTC.' : newEnd.problem }}
          </template>
          <template v-else-if="newEnd?.value">
            Until <span class="mono">{{ formatAbsoluteUtc(newEnd.value) }}</span>; refused afterwards.
          </template>
        </p>
      </div>

      <p v-if="changes.body" class="text-xs text-fg-muted" role="status">
        <template v-if="changes.body.applications">
          {{ changes.body.applications.length ? `Limited to ${listNames(changes.body.applications, 4)}.` : 'Every application.' }}
        </template>
        The token's value stays the same; the change is written to the audit trail with what it was before.
      </p>

      <p v-if="tooOld" class="rounded-sm border border-danger-line bg-danger-bg px-3 py-2 text-xs text-danger" role="alert">
        <span class="font-medium">This server cannot change a token yet.</span>
        Its agent is older than 0.7. Revoke the token and create another with what it should have, or upgrade the agent.
      </p>
      <InlineError v-else :error="error" />
    </form>
    <template #footer>
      <UiButton :disabled="pending" @click="emit('close')">
        Cancel
      </UiButton>
      <UiButton type="submit" form="token-edit-form" variant="primary" :pending="pending" :disabled="!changes.body" :title="changes.body ? undefined : changes.problem || 'Nothing has been changed'">
        Save changes
      </UiButton>
    </template>
  </UiDialog>
</template>
