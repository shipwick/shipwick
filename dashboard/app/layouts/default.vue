<script setup lang="ts">
import { summarizeAlerts, worstAlertTone } from '~/utils/alerts'

const route = useRoute()

// Which server this is, and whether it answers: shown at the top of the sidebar on every page.
const server = provideServerInfo()

const alerts = computed(() => server.data.value?.alerts ?? [])
const alertTone = computed(() => worstAlertTone(alerts.value))

// The mark in the navigation changes without a sound: a change in the alerts is said, on whichever page it happens.
const { announce } = useAnnounce()
watch(() => (server.data.value ? summarizeAlerts(alerts.value) : null), (summary, before) => {
  if (summary === null || before === null || before === undefined) return
  announce(alerts.value.length === 0 ? 'No active alerts any more.' : `The server now has ${summary}.`)
})

const drawer = ref<HTMLDialogElement | null>(null)
const drawerOpen = ref(false)
const trap = useFocusTrap(drawer)

function openDrawer() {
  drawerOpen.value = true
  drawer.value?.showModal()
}

function closeDrawer() {
  drawerOpen.value = false
  if (drawer.value?.open) drawer.value.close()
}

function onDrawerPointerDown(event: MouseEvent) {
  if (event.target === drawer.value) closeDrawer()
}

watch(() => route.fullPath, closeDrawer)
</script>

<template>
  <div class="min-h-dvh md:pl-56">
    <a
      href="#main"
      class="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-50 focus:rounded-sm focus:border focus:border-line-strong focus:bg-bg focus:px-3 focus:py-1.5"
    >Skip to content</a>

    <!-- Desktop: fixed narrow sidebar -->
    <aside aria-label="Sidebar" class="fixed inset-y-0 left-0 z-30 hidden w-56 border-r border-line bg-subtle md:block">
      <AppNav :server="server.data.value" :server-error="server.error.value" />
    </aside>

    <!-- Small screens: top bar + drawer -->
    <header class="flex h-12 items-center gap-2 border-b border-line bg-subtle px-2 md:hidden">
      <button
        type="button"
        class="target flex size-9 items-center justify-center rounded-sm text-fg-muted hover:bg-hover hover:text-fg"
        aria-label="Open navigation"
        :aria-expanded="drawerOpen"
        @click="openDrawer"
      >
        <UiIcon name="menu" />
      </button>
      <NuxtLink to="/" class="target flex items-center gap-2">
        <AppMark :size="16" compact />
        <span class="text-base font-semibold tracking-tight">Shipwick</span>
      </NuxtLink>
      <!-- The sidebar's alert mark is out of sight here: the same fact, in the bar. -->
      <NuxtLink
        v-if="alertTone"
        to="/servers"
        class="target ml-auto mr-1 inline-flex h-7 items-center gap-1.5 rounded-sm border px-2 text-xs font-medium"
        :class="alertTone === 'danger' ? 'border-danger-line bg-danger-bg text-danger' : 'border-warn-line bg-warn-bg text-warn'"
      >
        <UiIcon name="alert" :size="12" />
        {{ summarizeAlerts(alerts) }}
      </NuxtLink>
    </header>

    <dialog
      ref="drawer"
      aria-label="Navigation"
      class="m-0 h-dvh max-h-none w-64 max-w-[85vw] border-r border-line bg-subtle p-0 text-fg backdrop:bg-overlay md:hidden"
      @close="drawerOpen = false"
      @mousedown="onDrawerPointerDown"
      @keydown="trap.onKeydown"
    >
      <AppNav v-if="drawerOpen" :server="server.data.value" :server-error="server.error.value" @navigate="closeDrawer" />
    </dialog>

    <main id="main" tabindex="-1" class="@container/page outline-none">
      <slot />
    </main>
  </div>
</template>
