// Runs biosimage.wasm (cmd/wasm) off the page's main thread, so the page stays responsive
// while firmware is decompressed or recompressed. Messages from the page:
//   {id, fn, args}  calls biosimage[fn](...args)
// and back to it:
//   {ready: true} | {initError}            once, after loading the module
//   {id, progress: {op, done, total}}      while a call runs (LZMA on large data)
//   {id, result}                           when it is done; Uint8Arrays are transferred
importScripts('wasm_exec.js');

let current = 0; // id of the call being run, for progress messages
self.biosimageProgress = (op, done, total) => postMessage({ id: current, progress: { op, done, total } });

async function init() {
  const version = new URL(location.href).searchParams.get('v') || '';
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
  if (!self.biosimage) throw new Error('The WebAssembly module did not initialise.');
}

// buffers returns the ArrayBuffers of the Uint8Arrays in a result, to transfer instead of copy.
function buffers(v, out = new Set()) {
  if (v instanceof Uint8Array) out.add(v.buffer);
  else if (v && typeof v === 'object') for (const k in v) buffers(v[k], out);
  return [...out];
}

const ready = init();
ready.then(() => postMessage({ ready: true }), (err) => postMessage({ initError: err.message }));

onmessage = async ({ data: { id, fn, args } }) => {
  await ready;
  current = id;
  const result = biosimage[fn](...args);
  postMessage({ id, result }, buffers(result));
};
