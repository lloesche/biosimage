// Loads biosimage.wasm (cmd/wasm), which installs the global `biosimage` API.

import { $, setError } from './ui.js';

async function loadWasm() {
  if (typeof WebAssembly !== 'object') throw new Error('This browser does not support WebAssembly.');
  const version = (await fetch('version.txt').then((r) => (r.ok ? r.text() : '')).catch(() => '')).trim();
  $('version').textContent = /^\d/.test(version) ? `v${version}` : version;
  const resp = await fetch('biosimage.wasm' + (version ? `?v=${encodeURIComponent(version)}` : ''));
  if (!resp.ok) throw new Error(`Could not load biosimage.wasm (HTTP ${resp.status}).`);
  const go = new Go();
  let instance;
  try {
    ({ instance } = await WebAssembly.instantiate(await resp.arrayBuffer(), go.importObject));
  } catch (err) {
    throw new Error(`Your browser could not start the WebAssembly module (${err.message}). ` +
      'biosimage needs Chrome/Edge 79+, Firefox 64+ or Safari 15+.');
  }
  go.run(instance); // runs until main blocks; the API is installed synchronously
  if (!globalThis.biosimage) throw new Error('The WebAssembly module did not initialise.');
}

// ready resolves once the API can be used.
export const ready = loadWasm().catch((err) => {
  setError($('wasm-error'), err.message);
  throw err;
});
