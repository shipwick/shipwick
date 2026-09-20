<script setup lang="ts">
import type { Server } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import type { IconName } from '~/components/UiIcon.vue'

/** Sidebar content; rendered in the fixed sidebar on desktop and in the drawer on small screens. */
const props = defineProps<{
  server: Server | null
  serverError: AgentError | null
}>()

const emit = defineEmits<{ navigate: [] }>()

const route = useRoute()
const session = useSession()
const signingOut = ref(false)

const ITEMS: { to: string, label: string, icon: IconName, match: (path: string) => boolean }[] = [
  { to: '/', label: 'Overview', icon: 'overview', match: p => p === '/' },
  { to: '/applications', label: 'Applications', icon: 'applications', match: p => p.startsWith('/applications') },
  { to: '/deployments', label: 'Deployments', icon: 'deployments', match: p => p.startsWith('/deployments') },
  { to: '/servers', label: 'Servers', icon: 'servers', match: p => p.startsWith('/servers') },
  { to: '/logs', label: 'Logs', icon: 'logs', match: p => p.startsWith('/logs') },
]

async function signOut() {
  signingOut.value = true
  await session.logout()
}
</script>

<template>
  <div class="flex h-full flex-col">
    <NuxtLink to="/" class="flex h-12 shrink-0 items-center gap-2 border-b border-line px-4" @click="emit('navigate')">
      <AppMark />
      <span class="text-base font-semibold tracking-tight">Shipwick</span>
    </NuxtLink>

    <nav aria-label="Main" class="flex-1 overflow-y-auto p-2">
      <ul class="space-y-px">
        <li v-for="item in ITEMS" :key="item.to">
          <NuxtLink
            :to="item.to"
            class="flex h-8 items-center gap-2.5 rounded-sm px-2 transition-colors duration-100"
            :class="item.match(route.path) ? 'bg-active font-medium text-fg' : 'text-fg-muted hover:bg-hover hover:text-fg'"
            :aria-current="item.match(route.path) ? 'page' : undefined"
            @click="emit('navigate')"
          >
            <UiIcon :name="item.icon" />
            {{ item.label }}
          </NuxtLink>
        </li>
      </ul>
    </nav>

    <div class="shrink-0 space-y-3 border-t border-line p-3">
      <NuxtLink to="/servers" class="block rounded-sm px-1 py-0.5 hover:bg-hover" @click="emit('navigate')">
        <span class="label block">Server</span>
        <span v-if="props.server && !props.serverError" class="flex items-center gap-1.5">
          <span class="size-1.5 shrink-0 rounded-full bg-ok-dot" aria-hidden="true" />
          <span class="mono truncate text-xs" :title="props.server.hostname">{{ props.server.hostname }}</span>
        </span>
        <span v-else-if="props.serverError" class="flex items-center gap-1.5 text-danger">
          <span class="size-1.5 shrink-0 rounded-full bg-danger-dot" aria-hidden="true" />
          <span class="text-xs">{{ props.serverError.unreachable ? 'Agent unreachable' : 'Not responding' }}</span>
        </span>
        <span v-else class="skeleton mt-1.5 w-24" />
      </NuxtLink>

      <div class="flex items-center justify-between gap-2">
        <ThemeToggle />
        <UiButton variant="ghost" size="sm" :pending="signingOut" @click="signOut">
          <UiIcon name="logout" :size="14" />
          Sign out
        </UiButton>
      </div>
    </div>
  </div>
</template>
