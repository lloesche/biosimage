// biosimage web app. Everything runs locally: the firmware parser and patcher are the Go
// package compiled to WebAssembly (cmd/wasm, run by worker.js), the new logo is drawn with the
// browser's canvas. This module loads the BIOS (step 1) and patches it (step 3); editor.js is
// the New Logo step, explorer.js the firmware explorer.

import { $, MIME, el, fmtBytes, fmtNum, hex8, initHelp, objectURL, setError, setLocked, setupDrop, showHelp } from './ui.js';
import { fileHeaderView } from './hex.js';
import { blocked, initEditor, output, setDetectedFormats, setFirmware, setFreeSpace, storeOptions } from './editor.js';
import { initExplorer } from './explorer.js';
import { initNav, updateTabs } from './nav.js';
import { emit, events, state } from './state.js';
import { call } from './wasm.js';

const DEFAULT_HELP =
  'Replace the boot logo in an AMI Aptio / UEFI BIOS file.\n\n' +
  '1. Load the BIOS file from your board vendor.\n' +
  '2. Choose and place the new logo.\n' +
  '3. Create, verify and download the patched BIOS.\n\n' +
  'Everything happens in this browser tab. Nothing is uploaded.';

function outputName(name) {
  const dot = name.lastIndexOf('.');
  return dot > 0 ? `${name.slice(0, dot)}-logo${name.slice(dot)}` : `${name}-logo`;
}

// Don't let the browser navigate away when a file misses the drop zone.
window.addEventListener('dragover', (e) => e.preventDefault());
window.addEventListener('drop', (e) => e.preventDefault());

// ---------- open images ----------
//
// The loaded BIOS and the patched one are open in the worker, parsed once for every use. This
// module owns them: it announces a new one with the firmware or patched event and then closes
// the one it replaces.

let patchedId = 0;

function useFirmware(id) {
  const old = state.fwId;
  state.fwId = id;
  emit('firmware', id);
  if (old) call('close', [old]);
}

function usePatched(id) {
  const old = patchedId;
  patchedId = id;
  emit('patched', id);
  if (old) call('close', [old]);
}

// ---------- step 1: firmware ----------

let fwLoads = 0; // to drop results for a BIOS file that has been replaced by another one

async function loadFirmware(file) {
  const load = ++fwLoads;
  setError($('fw-error'));
  $('fw-info').hidden = true;
  invalidate();
  let info;
  try {
    info = await call('open', [new Uint8Array(await file.arrayBuffer())], `Reading ${file.name}`);
  } catch {
    return; // the WebAssembly module did not start; wasm.js shows why
  }
  if (load !== fwLoads) {
    call('close', [info.id]);
    return;
  }
  // A file with firmware volumes can still be explored, e.g. to see why no logo was found.
  const explorable = info.volumes > 0;
  useFirmware(explorable ? info.id : 0);
  if (!explorable) call('close', [info.id]);
  setLocked('step-explore', !explorable);
  if (info.error) {
    state.fwInfo = null;
    setLocked('step-img', true);
    setLocked('step-build', true);
    $('fw-drop-main').textContent = '[ Drop BIOS file here ]';
    setError($('fw-error'), explorable
      ? `${file.name}: ${info.error}`
      : `${file.name} does not look like a UEFI BIOS file (no firmware volumes found).`);
    updateTabs();
    return;
  }
  Object.assign(state, { fwName: file.name, fwInfo: info });
  $('fw-drop-main').textContent = `[ ${file.name} ]`;
  renderFirmware(file);
  setFirmware();
  setLocked('step-img', false);
  updateBuild();
  updateTabs();
  showHelp('BIOS file loaded.\n\nNext: drop the new logo image under “New Logo”.');
  // Reading the logo code for its formats may decompress the whole image; it runs in the
  // background.
  const res = await call('logoFormats', [info.id]);
  if (load !== fwLoads || res.error) return;
  setDetectedFormats(res.formats);
  setFreeSpace(res.freeSpace);
}

// container returns the innermost file holding the compressed volume a logo is in, if any.
const container = (logo) => logo.enclosing.filter((h) => h.compressed).pop();

function renderFirmware(file) {
  const info = state.fwInfo;
  const logo = info.logos[0];
  const cap = info.capsule;
  const outer = container(logo);
  const parent = logo.enclosing[logo.enclosing.length - 1];
  const facts = [
    ['File', `${file.name} · ${fmtNum(file.size)} bytes`],
    ['Format', !cap ? 'Flash image' : cap.headerSize ? `${cap.kind} · ${hex8(cap.headerSize).replace(/^0x0+/, '0x')} header` : `Flash image · ${cap.kind}`],
    cap?.signed ? ['Signature', `SIGNED – ${info.flashNote}`, 'alert'] : ['Signature', info.flashNote],
    ...(info.bootGuard ? [['Boot Guard', info.bootGuard, 'alert']] : []),
    ['Volumes', `${info.volumes} firmware volumes`],
    ['Logo File', logo.guid],
    ['Location', outer
      ? `${hex8(logo.fileOffset)} in compressed volume (file ${outer.guid.slice(0, 8)} @ ${hex8(outer.fileOffset)})`
      : `${hex8(logo.fileOffset)} · FV ${hex8(logo.volumeOffset)}, ${fmtBytes(logo.volumeLength)}` +
        (parent ? ` in file ${parent.guid.slice(0, 8)}` : '')],
    ['Stored As', `${logo.storage} · ${fmtNum(logo.fileSize)} bytes`],
  ];
  if (logo.imageInfo) {
    const i = logo.imageInfo;
    facts.push(['Current Logo', `${i.format} ${i.width}×${i.height} · ${i.detail}`]);
  }
  facts.push(['Capacity', `${fmtNum(logo.capacity)} bytes`]);
  if (outer) {
    facts.push(['Note', `The logo is inside the compressed main volume (${fmtBytes(outer.fileSize)}); ` +
      'patching recompresses it and takes a few seconds.']);
  }
  if (info.logos.length > 1) facts.push(['Copies', `${info.logos.length} (all will be replaced)`]);

  $('fw-facts').replaceChildren(...facts.flatMap(([k, v, cls]) => [el('dt', { textContent: k }), el('dd', { textContent: v, className: cls || '' })]));

  const img = $('fw-logo');
  if (logo.image && logo.imageInfo) {
    img.src = objectURL('fwLogo', logo.image, MIME[logo.imageInfo.format]);
    img.hidden = false;
    $('fw-logo-caption').textContent = `Current: ${logo.name || 'logo'}`;
  } else {
    img.hidden = true;
    $('fw-logo-caption').textContent = 'The current logo cannot be previewed.';
  }
  fileHeaderView($('fw-hex'), `Hex View – logo file @ ${hex8(logo.fileOffset)}${logo.compressed ? ' (decompressed)' : ''}`,
    logo.header, logo.fileOffset);
  $('fw-info').hidden = false;
}

// ---------- step 3: build ----------

// inputs counts changes to the BIOS file or the logo, to drop results computed for older ones.
let inputs = 0;

// invalidate drops the patched file once the inputs change.
function invalidate() {
  inputs++;
  $('result').hidden = true;
  state.built = false;
  if (patchedId) usePatched(0);
  setError($('build-error'));
  updateTabs();
}

function updateBuild() {
  $('build').disabled = !state.fwInfo || !state.source || blocked();
}

async function build() {
  if ($('build').disabled) return;
  setError($('build-error'));
  const btn = $('build');
  btn.disabled = true;
  btn.firstElementChild.textContent = 'Working…';
  const at = inputs;
  try {
    // replaceLogo verifies the result, including a fresh parse that must read back the image.
    const opts = { ...storeOptions(), bootGuardOK: $('bootguard-ok').checked };
    const res = await call('replaceLogo', [state.fwId, await output(), opts], 'Patching the BIOS');
    if (res.error) throw new Error(res.error);
    if (at === inputs) showResult(res);
    else call('close', [res.id]);
  } catch (err) {
    if (at === inputs) setError($('build-error'), `Could not patch the BIOS: ${err.message}`);
  } finally {
    btn.firstElementChild.textContent = 'Create Patched BIOS';
    updateBuild();
  }
}

function showResult(res) {
  const c = res.changes[0];
  const capacity = res.capacity;
  $('usage-bar').style.width = `${Math.min(100, (c.newSize / capacity) * 100).toFixed(1)}%`;
  $('usage-size').textContent = `${fmtNum(c.newSize)} / ${fmtNum(capacity)} bytes`;
  $('usage-text').textContent = [`${hex8(c.newOffset)} · ${c.storage}, ${c.placement}`,
    ...(res.changes.length > 1 ? [`${res.changes.length} copies`] : []), ...c.recompressed].join(' · ');
  $('bootguard-k').hidden = $('bootguard-v').hidden = !res.bootGuard;
  $('bootguard-v').textContent = res.bootGuard;

  const back = res.logos[0];
  $('readback').src = objectURL('readback', back.image, MIME[back.imageInfo?.format] || 'image/bmp');
  fileHeaderView($('new-hex'), `Hex View – new logo file @ ${hex8(back.fileOffset)}${back.compressed ? ' (decompressed)' : ''}`,
    back.header, back.fileOffset);

  const a = $('download');
  const name = outputName(state.fwName);
  a.href = objectURL('download', res.data, 'application/octet-stream');
  a.download = name;
  $('download-name').textContent = `${name} · ${fmtBytes(res.data.length)}`;

  renderFlashHelp();
  $('result').hidden = false;
  usePatched(res.id);
  state.built = true;
  updateTabs();
  $('result').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

function renderFlashHelp() {
  const { capsule: cap, flashNote } = state.fwInfo;
  const asus = cap?.kind === 'AMI Aptio capsule' && /\.cap$/i.test(state.fwName);
  const box = $('flash-help');
  box.replaceChildren(el('p', { textContent: flashNote }));
  let help = flashNote;
  if (asus) {
    const steps = [
      'Copy only the downloaded file to a FAT32 USB stick.',
      'Rename it there with BIOSRenamer.exe from your BIOS download.',
      'PC off: plug the stick into the BIOS FlashBack port and hold the button until the LED blinks.',
      'Wait until the LED stops. BIOS settings are reset to defaults.',
    ];
    box.append(el('p', { textContent: 'USB BIOS FlashBack:' }), el('ol', {}, ...steps.map((s) => el('li', { textContent: s }))));
    help += '\n\n' + steps.map((s, i) => `${i + 1}. ${s}`).join('\n');
  }
  $('download').dataset.help = help;
  showHelp(help);
}

// ---------- wiring ----------

initHelp();
initNav();
initEditor();
initExplorer();
setupDrop($('fw-drop'), $('fw-input'), loadFirmware);
events.addEventListener('logo', () => {
  invalidate();
  if (state.fwInfo && state.source) setLocked('step-build', false);
});
events.addEventListener('warnings', updateBuild);
$('build').addEventListener('click', build);

// Save & Download: F10 like in a real BIOS setup, plus Ctrl+S / Cmd+S because function keys
// often need Fn or are taken by the OS (macOS, many laptops).
const isMac = /Mac|iPhone|iPad/.test(navigator.userAgentData?.platform || navigator.platform || '');
const saveKey = isMac ? '⌘S' : 'Ctrl+S';
for (const node of document.querySelectorAll('.save-key')) node.textContent = saveKey;
document.addEventListener('keydown', async (e) => {
  const save = e.key === 'F10' || ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 's');
  if (!save) return;
  e.preventDefault();
  if (!state.built) await build();
  if (state.built) $('download').click();
});

setLocked('step-img', true);
setLocked('step-build', true);
setLocked('step-explore', true);
updateTabs();
showHelp(DEFAULT_HELP);
