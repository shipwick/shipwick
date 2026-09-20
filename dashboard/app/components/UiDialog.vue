<script setup lang="ts">
/**
 * Modal dialog on the native <dialog> element. showModal() gives us, for free
 * and correctly: focus moved inside and kept inside (everything else is
 * inert), Esc to close, focus returned to the opener on close, top-layer
 * stacking. Mark the field that should receive focus with `autofocus`.
 */
const props = withDefaults(defineProps<{
  open: boolean
  title: string
  /** While true the dialog cannot be dismissed (a request is in flight). */
  busy?: boolean
  size?: 'sm' | 'md'
}>(), { busy: false, size: 'sm' })

const emit = defineEmits<{ close: [] }>()

const dialog = ref<HTMLDialogElement | null>(null)
const titleId = useId()

function sync(open: boolean) {
  const el = dialog.value
  if (!el) return
  if (open && !el.open) el.showModal()
  else if (!open && el.open) el.close()
}

watch(() => props.open, open => sync(open), { flush: 'post' })
onMounted(() => sync(props.open))

function requestClose() {
  if (!props.busy) emit('close')
}

// Esc: the browser would close the dialog by itself; route it through the parent's state instead.
function onCancel(event: Event) {
  event.preventDefault()
  requestClose()
}

// The dialog element itself is only reachable by pointer where the backdrop is.
function onPointerDown(event: MouseEvent) {
  if (event.target === dialog.value) requestClose()
}
</script>

<template>
  <dialog
    ref="dialog"
    :aria-labelledby="titleId"
    class="m-auto w-[calc(100vw-2rem)] rounded-sm border border-line-strong bg-bg p-0 text-fg shadow-dialog backdrop:bg-overlay"
    :class="props.size === 'md' ? 'max-w-[36rem]' : 'max-w-[27rem]'"
    @cancel="onCancel"
    @mousedown="onPointerDown"
  >
    <div v-if="props.open" class="flex max-h-[calc(100dvh-4rem)] flex-col">
      <header class="flex items-start justify-between gap-4 border-b border-line px-5 py-3.5">
        <h2 :id="titleId" class="text-base font-semibold">
          {{ props.title }}
        </h2>
        <button
          type="button"
          class="-mr-1.5 -mt-0.5 rounded-sm p-1.5 text-fg-subtle hover:bg-hover hover:text-fg"
          aria-label="Close"
          :disabled="props.busy"
          @click="requestClose"
        >
          <UiIcon name="close" :size="14" />
        </button>
      </header>
      <div class="overflow-y-auto px-5 py-4">
        <slot />
      </div>
      <footer v-if="$slots.footer" class="flex flex-wrap items-center justify-end gap-2 border-t border-line bg-subtle px-5 py-3">
        <slot name="footer" />
      </footer>
    </div>
  </dialog>
</template>
