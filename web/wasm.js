// Runs biosimage.wasm (cmd/wasm) in a Web Worker (worker.js) and calls its API. Calls are
// queued and run one at a time; a call given a label shows a "please wait" box with its
// progress if it takes a moment.

import { $, fmtBytes, setError } from './ui.js';

const BUSY_DELAY = 200; // ms before the box appears, so quick calls do not flash it

let worker;
let seq = 0;
const pending = new Map(); // id -> { resolve, label, timer }

async function start() {
  if (typeof WebAssembly !== 'object') throw new Error('This browser does not support WebAssembly.');
  const version = (await fetch('version.txt').then((r) => (r.ok ? r.text() : '')).catch(() => '')).trim();
  $('version').textContent = /^\d/.test(version) ? `v${version}` : version;
  worker = new Worker('worker.js' + (version ? `?v=${encodeURIComponent(version)}` : ''));
  await new Promise((resolve, reject) => {
    worker.onerror = (e) => reject(new Error(`Could not start the web worker (${e.message || 'unknown error'}).`));
    worker.onmessage = ({ data }) => {
      if (data.ready) resolve();
      else if (data.initError) reject(new Error(data.initError));
    };
  });
  worker.onmessage = ({ data }) => {
    const p = pending.get(data.id);
    if (!p) return;
    if (data.progress) {
      progress(p, data.progress);
      return;
    }
    pending.delete(data.id);
    clearTimeout(p.timer);
    updateBusy();
    p.resolve(data.result);
  };
}

// ready resolves once the API can be used.
export const ready = start().catch((err) => {
  setError($('wasm-error'), err.message);
  throw err;
});

// call runs biosimage[fn](...args) in the worker and resolves to its result ({error} on
// failure, as returned by the Go side). With a label, a busy box shows while it runs. The
// ArrayBuffers in transfer are moved to the worker instead of copied, and unusable afterwards.
export async function call(fn, args = [], label = '', transfer = []) {
  await ready;
  const id = ++seq;
  return new Promise((resolve) => {
    const p = { resolve, label, shown: false };
    if (label) p.timer = setTimeout(() => {
      p.shown = true;
      updateBusy();
    }, BUSY_DELAY);
    pending.set(id, p);
    worker.postMessage({ id, fn, args }, transfer);
  });
}

// ---------- busy box ----------

// updateBusy shows the box for the oldest labelled call that has been running for a moment.
function updateBusy() {
  const p = [...pending.values()].find((c) => c.shown);
  $('busy').hidden = !p;
  if (!p) return;
  $('busy-text').textContent = `${p.label}…`;
  setBar(p.progress);
}

function progress(p, prog) {
  p.progress = prog;
  if (p.shown && [...pending.values()].find((c) => c.shown) === p) setBar(prog);
}

// setBar shows the progress of the current LZMA operation, or an indeterminate bar.
function setBar(prog) {
  const bar = $('busy-bar');
  bar.classList.toggle('indeterminate', !prog);
  if (!prog) {
    $('busy-sub').textContent = '';
    return;
  }
  const pct = Math.floor((prog.done / prog.total) * 100);
  bar.style.setProperty('--p', `${pct}%`);
  $('busy-sub').textContent = `${prog.op} ${fmtBytes(prog.total)} · ${pct}%`;
}
