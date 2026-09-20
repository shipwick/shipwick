<template>
  <div class="flex min-h-dvh flex-col bg-subtle">
    <div class="flex justify-end p-3">
      <ThemeToggle />
    </div>
    <main class="flex flex-1 items-start justify-center px-4 pb-16 pt-[12vh]">
      <slot />
    </main>
  </div>
</template>
