import type { ComputedRef, InjectionKey } from 'vue'

/** How many applications this server holds stopped for a promotion; 0 on a server that is not a standby. Provided by the server page. */
export const STANDBY_WAITING: InjectionKey<ComputedRef<number>> = Symbol('shipwick:standby-waiting')

export function useStandbyWaiting(): ComputedRef<number> {
  return inject(STANDBY_WAITING, computed(() => 0))
}
