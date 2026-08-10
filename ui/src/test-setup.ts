import "@testing-library/jest-dom/vitest";

// Node >= 24 ships Web Storage globals on by default; they shadow jsdom's
// window.localStorage with a stub that has no .clear()/.getItem(). Point the
// global back at jsdom's implementation so localStorage behaves the same in
// tests regardless of the Node version running them.
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
