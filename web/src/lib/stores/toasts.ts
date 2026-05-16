// Toast notification store. Components dispatch toasts.add({...}); a
// single <Toaster /> in the layout renders them. Toasts auto-dismiss
// after ttl ms (default 4 s) unless `dismissible: false` is set.
import { writable } from 'svelte/store';

export type ToastTone = 'success' | 'error' | 'info' | 'warning';

export interface Toast {
  id: number;
  title: string;
  body?: string;
  tone: ToastTone;
  ttl: number; // milliseconds; 0 means sticky
}

interface AddOpts {
  title: string;
  body?: string;
  tone?: ToastTone;
  ttl?: number;
}

function createToasts() {
  const { subscribe, update } = writable<Toast[]>([]);
  let nextId = 1;
  const timers = new Map<number, ReturnType<typeof setTimeout>>();

  function dismiss(id: number) {
    const t = timers.get(id);
    if (t) {
      clearTimeout(t);
      timers.delete(id);
    }
    update((list) => list.filter((x) => x.id !== id));
  }

  function add(opts: AddOpts): number {
    const id = nextId++;
    const ttl = opts.ttl ?? 4000;
    const t: Toast = {
      id,
      title: opts.title,
      body: opts.body,
      tone: opts.tone ?? 'info',
      ttl
    };
    update((list) => [...list, t]);
    if (ttl > 0) {
      timers.set(
        id,
        setTimeout(() => dismiss(id), ttl)
      );
    }
    return id;
  }

  return {
    subscribe,
    add,
    success(title: string, body?: string) {
      return add({ title, body, tone: 'success' });
    },
    error(title: string, body?: string) {
      return add({ title, body, tone: 'error', ttl: 7000 });
    },
    info(title: string, body?: string) {
      return add({ title, body, tone: 'info' });
    },
    warning(title: string, body?: string) {
      return add({ title, body, tone: 'warning' });
    },
    dismiss
  };
}

export const toasts = createToasts();
