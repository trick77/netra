import "@testing-library/jest-dom/vitest";

// Node >= 24 ships a global Web Storage implementation that jsdom's own
// window.localStorage gets shadowed by before this file even runs (both
// `globalThis.localStorage` and `window.localStorage` already point at
// Node's stub, which has no .clear()/.getItem()/.setItem()). Replace it
// with a minimal in-memory Storage so tests behave the same regardless of
// the Node version running them.
class MemoryStorage implements globalThis.Storage {
  #data = new Map<string, string>();
  get length(): number {
    return this.#data.size;
  }
  clear(): void {
    this.#data.clear();
  }
  getItem(key: string): string | null {
    return this.#data.has(key) ? this.#data.get(key)! : null;
  }
  key(index: number): string | null {
    return Array.from(this.#data.keys())[index] ?? null;
  }
  removeItem(key: string): void {
    this.#data.delete(key);
  }
  setItem(key: string, value: string): void {
    this.#data.set(key, String(value));
  }
}
Object.defineProperty(globalThis, "localStorage", {
  value: new MemoryStorage(),
  configurable: true,
  writable: true,
});

// jsdom implements no layout and ships no ResizeObserver, and ChartPanel
// measures its card to decide how wide to draw its chart. A stub that never
// fires keeps every panel at the fallback width the charts were drawn at
// before that measurement existed, which is the size the tests assert.
if (typeof globalThis.ResizeObserver === "undefined") {
  class NoopResizeObserver implements globalThis.ResizeObserver {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  Object.defineProperty(globalThis, "ResizeObserver", {
    value: NoopResizeObserver,
    configurable: true,
    writable: true,
  });
}
