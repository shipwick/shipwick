<script setup lang="ts">
import type { AccessRule, AccessRuleKind, Application, GrantAccessRequest, PersonSession, Role, SignedOut } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { AgentError as AgentFailure, toAgentError } from '~/utils/agentError'
import { issuerHost, listNames, ruleSubject, ruleSubjectProblem } from '~/utils/access'
import { formatAbsoluteUtc, pluralize } from '~/utils/format'
import { ROLE_DESCRIPTIONS, ROLE_ORDER } from '~/utils/roles'

/**
 * People who sign in with their account at the company's provider: the rules
 * that give an address, a group or a whole domain a role, and who is signed
 * in right now. The provider itself is configured on the agent.
 */
const agent = useAgent()
const access = useAccess()
const server = useServerInfo()

/** An agent before 0.6 has no rules to list: the tab says so instead. */
function orNull<T>(request: (signal: AbortSignal) => Promise<T>) {
  return async (signal: AbortSignal): Promise<T | null> => {
    try {
      return await request(signal)
    }
    catch (cause) {
      if (cause instanceof AgentFailure && cause.code === 'ENDPOINT_NOT_FOUND') return null
      throw cause
    }
  }
}

const rules = usePolling<AccessRule[] | null>(orNull(signal => agent.get<AccessRule[]>('/access/rules', { signal })), { interval: 30_000 })
const sessions = usePolling<PersonSession[] | null>(orNull(signal => agent.get<PersonSession[]>('/access/sessions', { signal })), { interval: 30_000 })
const applications = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }), { interval: 60_000 })
const known = computed(() => (applications.data.value ?? []).map(a => a.name).sort())

const unsupported = computed(() => !rules.loading.value && rules.data.value === null && !rules.error.value)
const signIn = computed(() => server.data.value?.sign_in ?? null)

// --- grant ----------------------------------------------------------------------

const KINDS: readonly { value: AccessRuleKind, label: string, placeholder: string, help: string }[] = [
  { value: 'email', label: 'One person', placeholder: 'ada@example.com', help: 'The address the provider names for the account.' },
  { value: 'group', label: 'A group', placeholder: 'developers', help: 'A group of the provider\'s, written exactly as the provider sends it.' },
  { value: 'domain', label: 'Everyone at a domain', placeholder: 'example.com', help: 'Every address at the domain: the part after the @.' },
]

const kind = ref<AccessRuleKind>('email')
const subject = ref('')
const role = ref<Role>('read')
const limits = ref<string[]>([])
const granting = ref(false)
const grantError = shallowRef<AgentError | null>(null)
const granted = ref('')

watch(role, (value) => {
  if (value !== 'deploy') limits.value = []
})

const kindInfo = computed(() => KINDS.find(k => k.value === kind.value)!)
const cleanSubject = computed(() => (kind.value === 'group' ? subject.value.trim() : subject.value.trim().toLowerCase().replace(/^\*?@/, '')))
const subjectProblem = computed(() => ruleSubjectProblem(kind.value, cleanSubject.value))
/** Granting again for the same subject replaces its rule: the form says which it will be. */
const existing = computed(() => (rules.data.value ?? []).find(r => r.kind === kind.value && r.subject === cleanSubject.value) ?? null)
/** The rule the admin's own session rests on: changing it ends that session with its next request. */
const ownRule = computed(() => access.token.value?.kind === 'user' && kind.value === 'email' && cleanSubject.value === access.token.value.name)

async function grant() {
  if (granting.value || cleanSubject.value === '' || subjectProblem.value) return
  granting.value = true
  grantError.value = null
  granted.value = ''
  try {
    const replaced = existing.value !== null
    const body: GrantAccessRequest = {
      kind: kind.value,
      subject: cleanSubject.value,
      role: role.value,
      ...(role.value === 'deploy' && limits.value.length > 0 ? { applications: limits.value } : {}),
    }
    const rule = await agent.post<AccessRule>('/access/rules', { body })
    granted.value = `${replaced ? 'Changed' : 'Added'}: ${ruleSubject(rule)} has the ${rule.role} role${rule.applications.length ? ` for ${listNames(rule.applications, 4)}` : ''}.`
    subject.value = ''
    limits.value = []
    void rules.refresh()
    void sessions.refresh()
  }
  catch (cause) {
    grantError.value = toAgentError(cause)
  }
  finally {
    granting.value = false
  }
}

// --- revoke ---------------------------------------------------------------------

const revoking = ref<AccessRule | null>(null)
const revokePending = ref(false)
const revokeError = shallowRef<AgentError | null>(null)

async function revoke() {
  const target = revoking.value
  if (!target || revokePending.value) return
  revokePending.value = true
  revokeError.value = null
  try {
    await agent.del(`/access/rules/${target.id}`)
    revoking.value = null
    void rules.refresh()
    void sessions.refresh()
  }
  catch (cause) {
    revokeError.value = toAgentError(cause)
    if (revokeError.value.notFound) void rules.refresh()
  }
  finally {
    revokePending.value = false
  }
}

// --- signed in now --------------------------------------------------------------

const ending = ref<string | null>(null)
const endError = shallowRef<AgentError | null>(null)
const ended = ref('')

async function signOut(email: string) {
  if (ending.value) return
  ending.value = email
  endError.value = null
  ended.value = ''
  try {
    const answer = await agent.del(`/access/sessions/${encodeURIComponent(email)}`) as unknown as SignedOut | undefined
    const count = answer?.sessions ?? 0
    ended.value = `Signed ${email} out of ${pluralize(count, 'session')}.`
    void sessions.refresh()
  }
  catch (cause) {
    endError.value = toAgentError(cause)
  }
  finally {
    ending.value = null
  }
}
</script>

<template>
  <div v-if="unsupported" class="rounded-sm border border-line">
    <EmptyState title="This agent takes API tokens only">
      Signing in with an account at an OpenID Connect provider is offered by agents from 0.6 on. Upgrade the agent; tokens work as they always did.
    </EmptyState>
  </div>

  <div v-else class="space-y-8">
    <p v-if="signIn && !signIn.configured" class="flex items-start gap-2 rounded-sm border border-warn-line bg-warn-bg px-3 py-2 text-xs text-warn" role="status">
      <UiIcon name="alert" :size="14" class="mt-px" />
      <span class="min-w-0">
        <span class="font-medium">Signing in is not configured on this agent, so no rule applies yet.</span>
        Set <span class="mono">SHIPWICK_OIDC_ISSUER</span>, <span class="mono">SHIPWICK_OIDC_CLIENT_ID</span> and <span class="mono">SHIPWICK_OIDC_CLIENT_SECRET</span> in <span class="mono">/opt/shipwick/.env</span> on the server. The rules can be prepared before that.
      </span>
    </p>
    <p v-else-if="signIn" class="text-fg-muted">
      People sign in at <span class="mono text-fg">{{ issuerHost(signIn.issuer) }}</span>. Nobody gets in without a rule below; a session lasts ten hours.
    </p>

    <UiPanel title="Who may sign in" :meta="rules.data.value?.length ?? null">
      <TableSkeleton v-if="rules.loading.value" :rows="3" :columns="6" />
      <ErrorState v-else-if="rules.error.value && !rules.data.value" :error="rules.error.value" subject="the rules" :retrying="rules.refreshing.value" @retry="rules.refresh()" />
      <EmptyState v-else-if="(rules.data.value?.length ?? 0) === 0" title="No rules yet">
        Nobody can sign in through the provider until a rule gives them a role. Add one below: for one person, for a group, or for everyone at a domain.
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack">
          <thead>
            <tr>
              <th>Who</th>
              <th>Role</th>
              <th>Applications</th>
              <th>Granted</th>
              <th>By</th>
              <th class="right">
                <span class="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rules.data.value" :key="r.id">
              <td data-primary class="mono break-all font-medium">
                {{ ruleSubject(r) }}
              </td>
              <td data-label="Role" class="mono" :title="ROLE_DESCRIPTIONS[r.role] ?? undefined">
                {{ r.role }}
              </td>
              <td data-label="Applications" :class="r.applications.length ? 'mono' : 'text-fg-muted'" :title="r.applications.join(', ') || 'Not limited: every application'">
                {{ r.applications.length ? listNames(r.applications, 3) : 'all' }}
              </td>
              <td data-label="Granted" class="text-fg-muted">
                <TimeAgo :time="r.created_at" />
              </td>
              <td data-label="By" class="mono break-all text-fg-muted">
                {{ r.created_by || '—' }}
              </td>
              <td class="right">
                <UiButton variant="danger" size="sm" @click="revoking = r">
                  Revoke
                </UiButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
        The most specific rule decides: address, then groups, then domain.
      </p>
    </UiPanel>

    <UiPanel title="Give access">
      <form class="space-y-4 px-4 py-4" @submit.prevent="grant">
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-[14rem_minmax(0,1fr)_14rem]">
          <div>
            <label for="rule-kind" class="label block">For</label>
            <select id="rule-kind" v-model="kind" class="input mt-1.5">
              <option v-for="k in KINDS" :key="k.value" :value="k.value">
                {{ k.label }}
              </option>
            </select>
          </div>
          <div>
            <label for="rule-subject" class="label block">{{ kind === 'email' ? 'Address' : kind === 'group' ? 'Group' : 'Domain' }}</label>
            <input
              id="rule-subject"
              v-model="subject"
              type="text"
              class="input mono mt-1.5"
              :placeholder="kindInfo.placeholder"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              required
              :aria-invalid="subjectProblem !== '' || undefined"
              aria-describedby="rule-subject-help"
            >
            <p id="rule-subject-help" class="mt-1.5 text-xs" :class="subjectProblem ? 'text-danger' : existing ? 'text-warn' : 'text-fg-muted'">
              <template v-if="subjectProblem">
                {{ subjectProblem }}
              </template>
              <template v-else-if="existing">
                {{ ruleSubject(existing) }} has the {{ existing.role }} role already: this replaces that rule.
              </template>
              <template v-else>
                {{ kindInfo.help }}
              </template>
            </p>
          </div>
          <div>
            <label for="rule-role" class="label block">Role</label>
            <select id="rule-role" v-model="role" class="input mono mt-1.5">
              <option v-for="r in ROLE_ORDER" :key="r" :value="r">
                {{ r }}
              </option>
            </select>
            <p class="mt-1.5 text-xs text-fg-muted">
              {{ ROLE_DESCRIPTIONS[role] }}
            </p>
          </div>
        </div>

        <ApplicationsPicker
          id="rule-applications"
          v-model="limits"
          :known="known"
          :disabled="role !== 'deploy'"
          :disabled-reason="role === 'read' ? 'The read role changes nothing, so there is nothing to limit.' : 'The admin role is for the whole server; only the deploy role can be limited to applications.'"
        />

        <p v-if="ownRule" class="flex items-start gap-2 text-xs text-warn" role="status">
          <UiIcon name="alert" :size="14" class="mt-px" />
          This is the rule your own session rests on: if it gives you something else than you have now, you are signed out with your next request and sign in again with the new role.
        </p>

        <UiButton type="submit" variant="primary" :pending="granting" :disabled="cleanSubject === '' || subjectProblem !== ''">
          {{ existing ? 'Change access' : 'Give access' }}
        </UiButton>
        <p v-if="granted" class="flex items-center gap-2 font-medium text-ok" role="status">
          <UiIcon name="check" :size="14" />
          {{ granted }}
        </p>
        <InlineError :error="grantError" />
      </form>
    </UiPanel>

    <UiPanel title="Signed in now" :meta="sessions.data.value?.length ?? null">
      <TableSkeleton v-if="sessions.loading.value" :rows="2" :columns="6" />
      <ErrorState v-else-if="sessions.error.value && !sessions.data.value" :error="sessions.error.value" subject="the sessions" :retrying="sessions.refreshing.value" @retry="sessions.refresh()" />
      <EmptyState v-else-if="(sessions.data.value?.length ?? 0) === 0" title="Nobody is signed in through the provider">
        A person who signs in appears here for the ten hours their session lasts. Tokens are not sessions and are listed under API tokens.
      </EmptyState>
      <div v-else class="overflow-x-auto">
        <table class="data-table stack">
          <thead>
            <tr>
              <th>Who</th>
              <th>Role</th>
              <th>Applications</th>
              <th>Signed in</th>
              <th>Ends</th>
              <th>Last used</th>
              <th class="right">
                <span class="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in sessions.data.value" :key="s.id">
              <td data-primary class="mono break-all font-medium">
                {{ s.email }}<span v-if="access.token.value?.kind === 'user' && s.email === access.token.value.name" class="ml-2 font-sans text-xs font-normal text-fg-subtle">you</span>
              </td>
              <td data-label="Role" class="mono">
                {{ s.role }}
              </td>
              <td data-label="Applications" :class="s.applications.length ? 'mono' : 'text-fg-muted'" :title="s.applications.join(', ') || undefined">
                {{ s.applications.length ? listNames(s.applications, 3) : 'all' }}
              </td>
              <td data-label="Signed in" class="text-fg-muted">
                <TimeAgo :time="s.created_at" />
              </td>
              <td data-label="Ends" class="text-fg-muted" :title="formatAbsoluteUtc(s.expires_at)">
                <TimeAgo :time="s.expires_at" />
              </td>
              <td data-label="Last used" class="text-fg-muted" title="Kept to the minute">
                <TimeAgo v-if="s.last_used_at" :time="s.last_used_at" />
                <span v-else>never</span>
              </td>
              <td class="right">
                <UiButton size="sm" :pending="ending === s.email" title="Ends every session of this person; they can sign in again while a rule covers them" @click="signOut(s.email)">
                  Sign out
                </UiButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="ended" class="flex items-center gap-2 border-t border-line px-4 py-2 text-xs font-medium text-ok" role="status">
        <UiIcon name="check" :size="12" />
        {{ ended }}
      </p>
      <InlineError :error="endError" class="m-3" />
    </UiPanel>

    <ConfirmDialog
      :open="revoking !== null"
      :title="`Revoke ${revoking ? ruleSubject(revoking) : ''}`"
      confirm-label="Revoke access"
      danger
      :pending="revokePending"
      :error="revokeError"
      @close="revoking = null; revokeError = null"
      @confirm="revoke"
    >
      <span class="mono text-fg">{{ revoking ? ruleSubject(revoking) : '' }}</span> no longer gets the {{ revoking?.role }} role from this rule. Sessions that rest on this rule end with their next request. Someone another rule still covers keeps what that rule gives.
    </ConfirmDialog>
  </div>
</template>
