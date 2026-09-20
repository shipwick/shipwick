<script setup lang="ts">
const route = useRoute()

// Which server this is, and whether it answers: shown at the bottom of the sidebar on every page.
const server = provideServerInfo()

const drawer = ref<HTMLDialogElement | null>(null)
const drawerOpen = ref(false)

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
  <div class="min-h-dvh md:pl-52">
    <a
      href="#main"
      class="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-50 focus:rounded-sm focus:border focus:border-line-strong focus:bg-bg focus:px-3 focus:py-1.5"
    >Skip to content</a>

    <!-- Desktop: fixed narrow sidebar -->
    <aside class="fixed inset-y-0 left-0 z-30 hidden w-52 border-r border-line bg-subtle md:block">
      <AppNav :server="server.data.value" :server-error="server.error.value" />
    </aside>

    <!-- Small screens: top bar + drawer -->
    <header class="flex h-12 items-center gap-2 border-b border-line bg-subtle px-2 md:hidden">
      <button
        type="button"
        class="flex size-8 items-center justify-center rounded-sm text-fg-muted hover:bg-hover hover:text-fg"
        aria-label="Open navigation"
        :aria-expanded="drawerOpen"
        @click="openDrawer"
      >
        <UiIcon name="menu" />
      </button>
      <NuxtLink to="/" class="flex items-center gap-2">
        <AppMark :size="16" />
        <span class="text-base font-semibold tracking-tight">Shipwick</span>
      </NuxtLink>
    </header>

    <dialog
      ref="drawer"
      aria-label="Navigation"
      class="m-0 h-dvh max-h-none w-64 max-w-[85vw] border-r border-line bg-subtle p-0 text-fg backdrop:bg-overlay md:hidden"
      @close="drawerOpen = false"
      @mousedown="onDrawerPointerDown"
    >
      <AppNav v-if="drawerOpen" :server="server.data.value" :server-error="server.error.value" @navigate="closeDrawer" />
    </dialog>

    <main id="main" tabindex="-1" class="outline-none">
      <slot />
    </main>
  </div>
</template>
