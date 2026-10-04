<template>
  <div class="flex min-h-dvh flex-col bg-subtle">
    <header class="flex justify-end p-3">
      <ThemeToggle />
    </header>
    <main id="main" tabindex="-1" class="@container/page flex flex-1 items-start justify-center px-4 pb-16 pt-[12vh] outline-none">
      <slot />
    </main>
  </div>
</template>
