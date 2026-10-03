<script setup lang="ts">
import type { Application, CreatedToken, CreateTokenRequest, Role, Token } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import type { ExpiryChoice } from '~/utils/access'
import { EXPIRY_CHOICES, tokenExpiryDisplay, expiryFromChoice, listNames } from '~/utils/access'
import { formatAbsoluteUtc } from '~/utils/format'
import { ROLE_DESCRIPTIONS, ROLE_ORDER } from '~/utils/roles'

/** API tokens: the list, and the form that creates one — for some applications only, and with an end, where that is wanted. */
const agent = useAgent()
const access = useAccess()
const now = useNow()

const tokens = usePolling<Token[]>(signal => agent.get<Token[]>('/tokens', { signal }), { interval: 30_000 })
// The applications to limit a deploy token to; a name that is not deployed yet can be typed.
const applications = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }), { interval: 60_000 })
const known = computed(() => (applications.data.value ?? []).map(a => a.name).sort())

// --- create ---------------------------------------------------------------------

const NAME_PATTERN = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/

const name = ref('')
const role = ref<Role>('deploy')
const limits = ref<string[]>([])
const expiry = ref<ExpiryChoice>('never')
const expiryDate = ref('')
const creating = ref(false)
const createError = shallowRef<AgentError | null>(null)
/** Shown once, right after creation; gone with the next navigation or "Done". */
const created = shallowRef<CreatedToken | null>(null)
const copied = ref(false)

const trimmedName = computed(() => name.value.trim().toLowerCase())
const nameProblem = computed(() => {
  const value = trimmedName.value
  if (value === '') return ''
  if (value === 'root') return 'root is the token the agent is configured with; choose another name.'
  if (!NAME_PATTERN.test(value)) return 'Lowercase letters, digits and dashes, up to 40 characters, e.g. ci.'
  return ''
})

// Only a deploy token can be limited: a read token changes nothing, and admin is for the whole server.
watch(role, (value) => {
  if (value !== 'deploy') limits.value = []
})

const expires = computed(() => expiryFromChoice(expiry.value, expiryDate.value, now.value))
const today = computed(() => new Date(now.value).toISOString().slice(0, 10))
const ready = computed(() => trimmedName.value !== '' && nameProblem.value === '' && expires.value.problem === '')

async function create() {
  if (creating.value || !ready.value) return
  creating.value = true
  createError.value = null
  try {
    // Fields that are not used are left out: an agent before 0.6 refuses what it does not know.
    const body: CreateTokenRequest = {
      name: trimmedName.value,
      role: role.value,
      ...(role.value === 'deploy' && limits.value.length > 0 ? { applications: limits.value } : {}),
      ...(expires.value.value ? { expires_at: expires.value.value } : {}),
    }
    created.value = await agent.post<CreatedToken>('/tokens', { body })
    copied.value = false
    name.value = ''
    limits.value = []
    expiry.value = 'never'
    expiryDate.value = ''
    void tokens.refresh()
  }
  catch (cause) {
    createError.value = toAgentError(cause)
  }
  finally {
    creating.value = false
  }
}

/** An agent before 0.6 knows neither field and says so as an unknown field. */
const tooOld = computed(() => createError.value?.code === 'INVALID_REQUEST' && createError.value.message.includes('unknown field'))

async function copy() {
  if (!created.value) return
  try {
    await navigator.clipboard.writeText(created.value.token)
    copied.value = true
  }
  catch {
    // No clipboard access (http, or denied): the value is selectable right there.
  }
}

// --- revoke ---------------------------------------------------------------------

const revoking = ref<Token | null>(null)
const revokePending = ref(false)
const revokeError = shallowRef<AgentError | null>(null)

async function revoke() {
  const target = revoking.value
  if (!target || revokePending.value) return
  revokePending.value = true
  revokeError.value = null
  try {
    await agent.del(`/tokens/${encodeURIComponent(target.name)}`)
    revoking.value = null
    void tokens.refresh()
  }
  catch (cause) {
    revokeError.value = toAgentError(cause)
    // Already gone: the list on screen was stale.
    if (revokeError.value.notFound) void tokens.refresh()
  }
  finally {
    revokePending.value = false
  }
}

function closeRevoke() {
  revoking.value = null
  revokeError.value = null
}

const own = computed(() => (access.token.value?.kind === 'user' ? '' : access.token.value?.name ?? ''))
const EXPIRY_TEXT = { ok: 'text-fg-muted', muted: 'text-fg-muted', warn: 'text-warn', danger: 'text-danger' } as const
</script>

<template>
  <div class="space-y-8">
    <StaleNotice :error="tokens.data.value ? tokens.error.value : null" :updated-at="tokens.updatedAt.value" class="!mb-0" />

    <UiPanel title="API tokens" :meta="tokens.data.value?.length ?? null">
      <TableSkeleton v-if="tokens.loading.value" :rows="3" :columns="6" />
      <ErrorState
        v-else-if="tokens.error.value && !tokens.data.value"
        :error="tokens.error.value"
        subject="tokens"
        :retrying="tokens.refreshing.value"
        @retry="tokens.refresh()"
      />
      <EmptyState v-else-if="(tokens.data.value?.length ?? 0) === 0" title="No tokens besides root">
        Create one below for CI, a teammate or a read-only dashboard, so the root token can stay on the server.
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack">
          <thead>
            <tr>
              <th>Name</th>
              <th>Role</th>
              <th>Applications</th>
              <th>Expires</th>
              <th>Created</th>
              <th>Last used</th>
              <th class="right">
                <span class="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in tokens.data.value" :key="t.id">
              <td data-primary class="mono font-medium">
                {{ t.name }}<span v-if="t.name === own" class="ml-2 font-sans text-xs font-normal text-fg-subtle">this session</span>
              </td>
              <td data-label="Role" class="mono" :title="ROLE_DESCRIPTIONS[t.role] ?? undefined">
                {{ t.role }}
              </td>
              <td data-label="Applications" :class="t.applications?.length ? 'mono' : 'text-fg-muted'" :title="t.applications?.length ? t.applications.join(', ') : 'Not limited: every application'">
                {{ t.applications?.length ? listNames(t.applications, 3) : 'all' }}
              </td>
              <td data-label="Expires" :class="EXPIRY_TEXT[tokenExpiryDisplay(t.expires_at, now)?.tone ?? 'muted']" :title="t.expires_at ? formatAbsoluteUtc(t.expires_at) : 'This token does not expire'">
                <template v-if="tokenExpiryDisplay(t.expires_at, now)">
                  <span class="inline-flex items-center gap-1.5">
                    <UiIcon v-if="tokenExpiryDisplay(t.expires_at, now)!.soon" name="alert" :size="12" />
                    {{ tokenExpiryDisplay(t.expires_at, now)!.label }}
                  </span>
                </template>
                <template v-else>
                  never
                </template>
              </td>
              <td data-label="Created" class="text-fg-muted">
                <TimeAgo :time="t.created_at" />
              </td>
              <td data-label="Last used" class="text-fg-muted" title="Kept to the minute">
                <TimeAgo v-if="t.last_used_at" :time="t.last_used_at" />
                <span v-else>never</span>
              </td>
              <td class="right">
                <UiButton variant="danger" size="sm" @click="revoking = t">
                  Revoke
                </UiButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
        The root token is configured on the agent (<span class="mono">SHIPWICK_AGENT_TOKEN</span>, or generated on first start) and is not listed here: it has the admin role, does not expire and cannot be revoked through the API. A token that has expired stays listed until it is revoked, so that whoever holds it is told why it stopped working.
      </p>
    </UiPanel>

    <UiPanel title="New token">
      <div v-if="created" class="space-y-3 px-4 py-4" role="status">
        <p class="flex items-center gap-2 font-medium text-ok">
          <UiIcon name="check" :size="14" />
          Token <span class="mono">{{ created.name }}</span> created with the {{ created.role }} role
        </p>
        <div class="flex flex-wrap items-center gap-2">
          <code class="mono select-all break-all rounded-sm border border-line bg-inset px-2.5 py-1.5 text-sm">{{ created.token }}</code>
          <UiButton size="sm" @click="copy">
            <UiIcon name="copy" :size="12" />
            {{ copied ? 'Copied' : 'Copy' }}
          </UiButton>
        </div>
        <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
          <dt class="label pt-0.5">
            Applications
          </dt>
          <dd :class="created.applications?.length ? 'mono break-words' : 'text-fg-muted'">
            {{ created.applications?.length ? created.applications.join(', ') : 'all' }}
          </dd>
          <dt class="label pt-0.5">
            Expires
          </dt>
          <dd class="text-fg-muted">
            <template v-if="created.expires_at">
              <span class="mono text-fg">{{ formatAbsoluteUtc(created.expires_at) }}</span> ({{ tokenExpiryDisplay(created.expires_at, now)?.label }})
            </template>
            <template v-else>
              never
            </template>
          </dd>
        </dl>
        <p class="text-fg-muted">
          Copy it now: it will not be shown again. The agent keeps only a hash. Use it as <span class="mono text-fg">shipwick login --token</span> or as the token of a CI job.
        </p>
        <UiButton size="sm" variant="ghost" @click="created = null">
          Done
        </UiButton>
      </div>
      <form v-else class="space-y-4 px-4 py-4" @submit.prevent="create">
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_14rem_14rem]">
          <div>
            <label for="token-name" class="label block">Name</label>
            <input
              id="token-name"
              v-model="name"
              type="text"
              class="input mono mt-1.5"
              placeholder="ci"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              required
              :aria-invalid="nameProblem !== '' || undefined"
              aria-describedby="token-name-help"
            >
            <p id="token-name-help" class="mt-1.5 text-xs" :class="nameProblem ? 'text-danger' : 'text-fg-muted'">
              {{ nameProblem || 'Names events and deployments made with it: "Application stopped by ci".' }}
            </p>
          </div>
          <div>
            <label for="token-role" class="label block">Role</label>
            <select id="token-role" v-model="role" class="input mono mt-1.5">
              <option v-for="r in ROLE_ORDER" :key="r" :value="r">
                {{ r }}
              </option>
            </select>
            <p class="mt-1.5 text-xs text-fg-muted">
              {{ ROLE_DESCRIPTIONS[role] }}
            </p>
          </div>
          <div>
            <label for="token-expiry" class="label block">Expires</label>
            <select id="token-expiry" v-model="expiry" class="input mt-1.5">
              <option v-for="choice in EXPIRY_CHOICES" :key="choice.value" :value="choice.value">
                {{ choice.label }}
              </option>
            </select>
            <template v-if="expiry === 'date'">
              <label for="token-expiry-date" class="sr-only">The last day the token works</label>
              <input id="token-expiry-date" v-model="expiryDate" type="date" class="input mono mt-1.5" :min="today" :aria-invalid="(expiryDate !== '' && expires.problem !== '') || undefined">
            </template>
            <p class="mt-1.5 text-xs" :class="expiry === 'date' && expiryDate !== '' && expires.problem ? 'text-danger' : 'text-fg-muted'">
              <template v-if="expiry === 'never'">
                Works until it is revoked.
              </template>
              <template v-else-if="expires.problem">
                {{ expiryDate === '' ? 'Works until the end of that day, UTC.' : expires.problem }}
              </template>
              <template v-else>
                Until <span class="mono">{{ formatAbsoluteUtc(expires.value) }}</span>; refused afterwards.
              </template>
            </p>
          </div>
        </div>

        <ApplicationsPicker
          id="token-applications"
          v-model="limits"
          :known="known"
          :disabled="role !== 'deploy'"
          :disabled-reason="role === 'read' ? 'A read token changes nothing, so there is nothing to limit.' : 'The admin role is for the whole server; only a deploy token can be limited to applications.'"
        />

        <div class="flex flex-wrap items-center gap-3">
          <UiButton type="submit" variant="primary" :pending="creating" :disabled="!ready">
            Create token
          </UiButton>
        </div>
        <InlineError :error="createError" />
        <p v-if="tooOld" class="text-xs text-fg-muted">
          This agent is older than 0.6: it knows neither tokens for some applications nor tokens that expire. Create the token without them, or upgrade the agent.
        </p>
      </form>
    </UiPanel>

    <ConfirmDialog
      :open="revoking !== null"
      :title="`Revoke ${revoking?.name ?? ''}`"
      confirm-label="Revoke token"
      danger
      :pending="revokePending"
      :error="revokeError"
      @close="closeRevoke"
      @confirm="revoke"
    >
      Requests made with <span class="mono text-fg">{{ revoking?.name }}</span> are refused from now on; anything using it (a CI job, another dashboard) stops working until it gets a new token.
      <template v-if="revoking?.name === own">
        This is the token of your own session: you will be signed out.
      </template>
    </ConfirmDialog>
  </div>
</template>
