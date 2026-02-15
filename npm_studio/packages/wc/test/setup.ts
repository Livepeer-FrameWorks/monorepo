// Polyfill customElements for jsdom
if (typeof globalThis.customElements === "undefined") {
  // @ts-expect-error -- minimal polyfill for tests
  globalThis.customElements = {
    _registry: new Map<string, CustomElementConstructor>(),
    define(name: string, ctor: CustomElementConstructor) {
      this._registry.set(name, ctor);
    },
    get(name: string) {
      return this._registry.get(name);
    },
    whenDefined(_name: string) {
      return Promise.resolve(undefined as unknown as CustomElementConstructor);
    },
  };
}
