// The shell's local storage, every key prefixed with lodge: (shell.md,
// Rules). Storage may be unavailable or full; the shell works without it.

/** A key-value store, the page's localStorage by default. */
export interface Store {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export type SettingKey = "shortcut" | "theme" | "last";

export class Settings {
  readonly #store: Store | null;

  constructor(store?: Store | null) {
    let s = store;
    if (s === undefined) {
      try {
        s = globalThis.localStorage ?? null;
      } catch {
        s = null;
      }
    }
    this.#store = s;
  }

  get(key: SettingKey): string | null {
    try {
      return this.#store?.getItem(`lodge:${key}`) ?? null;
    } catch {
      return null;
    }
  }

  set(key: SettingKey, value: string | null): void {
    try {
      if (value === null) this.#store?.removeItem(`lodge:${key}`);
      else this.#store?.setItem(`lodge:${key}`, value);
    } catch {
      // Private mode or a full store: the setting lasts for this page only.
    }
  }
}
