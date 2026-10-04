<script setup lang="ts">
import type { Server } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { accessSummary, sessionExpiryWarning, signedInUntil } from '~/utils/access'
import { summarizeAlerts, worstAlertTone } from '~/utils/alerts'
import { NAV_GROUPS, isActive } from '~/utils/navigation'

/** Sidebar content; rendered in the fixed sidebar on desktop and in the drawer on small screens. */
const props = defineProps<{
  server: Server | null
  serverError: AgentError | null
}>()

const emit = defineEmits<{ navigate: [] }>()

const route = useRoute()
const session = useSession()
const access = useAccess()
const signingOut = ref(false)
const now = useNow()

// An admin-only entry appears once the role is known to be admin: a read-only token should not see it flash.
const groups = computed(() => NAV_GROUPS
  .map(group => ({ ...group, items: group.items.filter(item => !item.admin || access.knownTo('admin')) }))
  .filter(group => group.items.length > 0))

// Active alerts are visible from every page: a mark on the server's entry, where they are listed.
const alerts = computed(() => props.server?.alerts ?? [])
const alertTone = computed(() => worstAlertTone(alerts.value))

// The session's own end, once it is near: a token that stops working mid-week should not be a surprise.
const expiryWarning = computed(() => sessionExpiryWarning(access.token.value, now.value))

/** A person who signed in through the provider: when their session ends, instead of a warning. */
const until = computed(() => signedInUntil(access.token.value))

// The list of servers is a small popover: Escape and leaving it close it, as a menu would.
const switcher = ref<HTMLDetailsElement | null>(null)

function closeSwitcher(refocus: boolean) {
  const el = switcher.value
  if (!el?.open) return
  el.open = false
  if (refocus) el.querySelector('summary')?.focus()
}

function onSwitcherFocusOut(event: FocusEvent) {
  const next = event.relatedTarget
  if (next instanceof Node && !switcher.value?.contains(next)) closeSwitcher(false)
}

function onDocumentPointerDown(event: PointerEvent) {
  if (event.target instanceof Node && !switcher.value?.contains(event.target)) closeSwitcher(false)
}

onMounted(() => document.addEventListener('pointerdown', onDocumentPointerDown))
onBeforeUnmount(() => document.removeEventListener('pointerdown', onDocumentPointerDown))

async function signOut() {
  signingOut.value = true
  await session.logout(access.token.value?.kind === 'user')
}
</script>

<template>
  <div class="flex h-full flex-col">
    <NuxtLink to="/" class="flex h-12 shrink-0 items-center gap-2 border-b border-line px-4 focus-visible:-outline-offset-2" @click="emit('navigate')">
      <AppMark compact />
      <span class="text-base font-semibold tracking-tight">Shipwick</span>
    </NuxtLink>

    <!-- Which server all of this is about. With several servers, this is where one is chosen. -->
    <details v-if="session.multiple.value" ref="switcher" class="group relative mx-2 mt-2 shrink-0" @keydown.esc="closeSwitcher(true)" @focusout="onSwitcherFocusOut">
      <summary class="target flex cursor-pointer list-none items-center gap-2 rounded-sm border border-line bg-bg px-2.5 py-1.5 hover:border-line-strong [&::-webkit-details-marker]:hidden" title="The server this dashboard shows; choose another">
        <span class="size-1.5 shrink-0 rounded-full" :class="props.serverError ? 'bg-danger-dot' : props.server ? 'bg-ok-dot' : 'bg-muted-dot'" aria-hidden="true" />
        <span class="min-w-0 flex-1">
          <span class="block truncate text-xs font-medium"><span class="sr-only">Server </span>{{ session.selected.value }}</span>
          <span v-if="props.serverError" class="block truncate text-2xs text-danger">{{ props.serverError.unreachable ? 'Agent unreachable' : 'Not responding' }}</span>
          <span v-else-if="props.server" class="mono block truncate text-2xs text-fg-subtle" :title="props.server.hostname">{{ props.server.hostname }}<span class="sr-only">, connected</span></span>
        </span>
        <UiIcon name="chevron-right" :size="12" class="rotate-90 text-fg-subtle transition-transform duration-100 group-open:-rotate-90" />
      </summary>
      <div class="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-sm border border-line-strong bg-bg shadow-dialog">
        <p class="label px-2.5 pb-1 pt-2">
          Servers
        </p>
        <ul>
          <li v-for="s in session.servers.value" :key="s.name">
            <!-- A plain link: another server is loaded afresh, so nothing of this one stays on screen. -->
            <a
              :href="s.authenticated ? `/?server=${s.name}` : `/login?server=${s.name}`"
              class="target flex h-8 items-center gap-2 px-2.5 text-xs hover:bg-hover focus-visible:-outline-offset-2"
              :class="s.name === session.selected.value ? 'font-medium text-fg' : 'text-fg-muted hover:text-fg'"
              :aria-current="s.name === session.selected.value ? 'true' : undefined"
            >
              <span class="flex w-3 shrink-0 justify-center">
                <UiIcon v-if="s.name === session.selected.value" name="check" :size="12" />
              </span>
              <span class="min-w-0 flex-1 truncate">{{ s.name }}</span>
              <span v-if="!s.authenticated" class="shrink-0 text-2xs text-fg-subtle">sign in</span>
            </a>
          </li>
        </ul>
        <a href="/servers" class="target flex h-8 items-center gap-2 border-t border-line px-2.5 text-xs text-fg-muted hover:bg-hover hover:text-fg focus-visible:-outline-offset-2">
          <span class="w-3 shrink-0" />
          All servers
        </a>
      </div>
    </details>
    <NuxtLink v-else to="/servers" class="target mx-2 mt-2 flex shrink-0 items-center gap-2 rounded-sm border border-line bg-bg px-2.5 py-1.5 hover:border-line-strong" title="The server this dashboard shows" @click="emit('navigate')">
      <template v-if="props.server && !props.serverError">
        <span class="size-1.5 shrink-0 rounded-full bg-ok-dot" aria-hidden="true" />
        <span class="mono min-w-0 flex-1 truncate text-xs" :title="props.server.hostname">{{ props.server.hostname }}</span>
        <span class="sr-only">connected</span>
      </template>
      <template v-else-if="props.serverError">
        <span class="size-1.5 shrink-0 rounded-full bg-danger-dot" aria-hidden="true" />
        <span class="min-w-0 flex-1 truncate text-xs text-danger">{{ props.serverError.unreachable ? 'Agent unreachable' : 'Not responding' }}</span>
      </template>
      <span v-else class="skeleton my-1 w-24" /><span v-if="!props.server && !props.serverError" class="sr-only">Server</span>
    </NuxtLink>

    <nav aria-label="Main" class="flex-1 overflow-y-auto p-2">
      <div v-for="(group, index) in groups" :key="group.label" :class="index > 0 ? 'mt-4' : ''">
        <p v-if="group.label" :id="`nav-${group.label}`" class="label mb-1 px-2">
          {{ group.label }}
        </p>
        <ul class="space-y-px" :aria-labelledby="group.label ? `nav-${group.label}` : undefined">
          <li v-for="item in group.items" :key="item.to">
            <NuxtLink
              :to="item.to"
              class="target flex h-8 items-center gap-2.5 rounded-sm px-2 transition-colors duration-100"
              :class="isActive(item, route.path) ? 'bg-active font-medium text-fg' : 'text-fg-muted hover:bg-hover hover:text-fg'"
              :aria-current="isActive(item, route.path) ? 'page' : undefined"
              @click="emit('navigate')"
            >
              <UiIcon :name="item.icon" />
              {{ item.label }}
              <span
                v-if="item.to === '/servers' && alertTone"
                class="mono ml-auto inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-2xs font-medium"
                :class="alertTone === 'danger' ? 'bg-danger-solid text-[#fff]' : 'border border-warn-line bg-warn-bg text-warn'"
                :title="summarizeAlerts(alerts)"
              >
                {{ alerts.length }}<span class="sr-only"> {{ alerts.length === 1 ? 'alert' : 'alerts' }}</span>
              </span>
            </NuxtLink>
          </li>
        </ul>
      </div>
    </nav>

    <div class="shrink-0 space-y-2.5 border-t border-line p-3">
      <!-- Who is signed in, so what the buttons allow is never a surprise. -->
      <div v-if="access.token.value" class="px-1">
        <span class="label block">Signed in as</span>
        <span class="mono block truncate text-xs" :title="access.token.value.name">
          {{ access.token.value.name }} <span class="text-fg-muted">· {{ access.token.value.role }}</span>
        </span>
        <span class="block text-2xs text-fg-subtle">{{ accessSummary(access.token.value) }}<template v-if="until">, {{ until }}</template></span>
        <span v-if="expiryWarning" class="mt-1 flex items-start gap-1 text-2xs text-warn" role="status">
          <UiIcon name="alert" :size="12" class="mt-px" />
          {{ expiryWarning }}
        </span>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-2">
        <ThemeToggle />
        <UiButton variant="ghost" size="sm" :pending="signingOut" @click="signOut">
          <UiIcon name="logout" :size="14" />
          Sign out
        </UiButton>
      </div>
    </div>
  </div>
</template>
