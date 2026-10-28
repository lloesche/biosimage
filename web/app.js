// biosimage web app. Everything runs locally: the firmware parser and patcher are the Go
// package compiled to WebAssembly (cmd/wasm), image conversion uses the browser's canvas.

import { $, MIME, clamp, el, fmtBytes, fmtNum, hex8, initHelp, objectURL, setError, setLocked, showHelp } from './ui.js';
import { fileHeaderView } from './hex.js';
import { canvasToBMP, decodeImage, sizeSVG } from './image.js';
import { initExplorer } from './explorer.js';
import { initNav, updateTabs } from './nav.js';
import { emit, state } from './state.js';
import { ready } from './wasm.js';

// Firmware draws the logo unscaled; the preview shows it on a screen of this width.
const PREVIEW_SCREEN_WIDTH = 1920;
const MAX_W = 800;
const MAX_H = 600;
const DEFAULT_SIZE = [672, 378];

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

// ---------- drop zones ----------

function setupDrop(zone, input, onFile) {
  input.addEventListener('change', () => {
    if (input.files[0]) onFile(input.files[0]);
    input.value = '';
  });
  zone.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      input.click();
    }
  });
  zone.addEventListener('dragover', (e) => {
    e.preventDefault();
    zone.classList.add('over');
  });
  zone.addEventListener('dragleave', () => zone.classList.remove('over'));
  zone.addEventListener('drop', (e) => {
    e.preventDefault();
    zone.classList.remove('over');
    if (e.dataTransfer.files[0]) onFile(e.dataTransfer.files[0]);
  });
}

// Don't let the browser navigate away when a file misses the drop zone.
window.addEventListener('dragover', (e) => e.preventDefault());
window.addEventListener('drop', (e) => e.preventDefault());

// ---------- step 1: firmware ----------

async function loadFirmware(file) {
  setError($('fw-error'));
  $('fw-info').hidden = true;
  invalidate();
  try {
    await ready;
  } catch {
    return;
  }
  const bytes = new Uint8Array(await file.arrayBuffer());
  const info = biosimage.inspectFirmware(bytes);
  if (info.error) {
    state.fw = null;
    setLocked('step-img', true);
    setLocked('step-build', true);
    $('fw-drop-main').textContent = '[ Drop BIOS file here ]';
    // A file with firmware volumes can still be explored, e.g. to see why no logo was found.
    emit('firmware', info.volumes > 0 ? bytes : null);
    setLocked('step-explore', info.volumes === 0);
    setError($('fw-error'), info.volumes === 0
      ? `${file.name} does not look like a UEFI BIOS file (no firmware volumes found).`
      : `${file.name}: ${info.error}`);
    updateTabs();
    return;
  }
  Object.assign(state, { fw: bytes, fwName: file.name, fwInfo: info });
  $('fw-drop-main').textContent = `[ ${file.name} ]`;
  renderFirmware(file);
  emit('firmware', bytes);
  setLocked('step-explore', false);
  resetSize();
  setLocked('step-img', false);
  if (state.source) {
    setLocked('step-build', false);
    render();
  }
  updateTabs();
  showHelp('BIOS file loaded.\n\nNext: drop the new logo image under “New Logo”.');
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

// ---------- step 2: image ----------

async function loadImage(file) {
  setError($('img-error'));
  invalidate();
  const bytes = new Uint8Array(await file.arrayBuffer());
  const isSVG = file.type === 'image/svg+xml' || /\.svg$/i.test(file.name);
  let img;
  try {
    const blob = isSVG ? new Blob([sizeSVG(new TextDecoder().decode(bytes))], { type: 'image/svg+xml' }) : file;
    img = await decodeImage(blob);
  } catch {
    setError($('img-error'), `${file.name}: your browser cannot read this image.`);
    return;
  }
  const info = isSVG ? null : biosimage.inspectImage(bytes);
  state.source = { img, bytes, name: file.name, info: info && !info.error ? info : null };
  $('img-drop-main').textContent = `[ ${file.name} ]`;

  // "File as-is" only for formats the firmware can decode.
  const asis = document.querySelector('input[name=mode][value=asis]');
  asis.disabled = !state.source.info;
  if (asis.disabled && asis.checked) document.querySelector('input[name=mode][value=bmp]').checked = true;
  const si = state.source.info;
  $('mode-hint').textContent = !si
    ? `${file.name}: converted to BMP.`
    : si.format === 'GIF' && !si.detail.startsWith('1 ')
      ? `Animated GIF (${si.detail}): choose “File as-is” to keep the animation; conversion uses the first frame.`
      : `${file.name}: ${si.format} ${si.width}×${si.height}, ${si.detail}, ${fmtNum(bytes.length)} bytes.`;

  $('editor').hidden = false;
  if (state.fw) setLocked('step-build', false);
  render();
  updateTabs();
}

function mode() {
  return document.querySelector('input[name=mode]:checked').value;
}

function resetSize() {
  const i = state.fwInfo?.logos[0]?.imageInfo;
  const [w, h] = i ? [i.width, i.height] : DEFAULT_SIZE;
  $('cw').value = clamp(w, 16, MAX_W);
  $('ch').value = clamp(h, 16, MAX_H);
}

function updateControls() {
  const scale = $('scale');
  scale.style.setProperty('--p', `${((scale.value - scale.min) / (scale.max - scale.min)) * 100}%`);
  $('scale-out').textContent = `${scale.value}%`;
  for (const id of ['ox', 'oy']) {
    const v = Number($(id).value);
    $(`${id}-out`).textContent = v === 0 ? 'Centre' : `${v > 0 ? '+' : ''}${v}%`;
  }
  $('bg-hex').textContent = $('bg').value.toUpperCase();
}

function render() {
  updateControls();
  const src = state.source;
  if (!src) return;
  const canvas = $('canvas');
  const ctx = canvas.getContext('2d');
  const asis = mode() === 'asis';
  $('layout').disabled = asis;
  if (asis) {
    canvas.width = src.img.naturalWidth;
    canvas.height = src.img.naturalHeight;
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(src.img, 0, 0);
  } else {
    const w = clamp($('cw').value, 16, MAX_W);
    const h = clamp($('ch').value, 16, MAX_H);
    canvas.width = w;
    canvas.height = h;
    ctx.fillStyle = $('bg').value;
    ctx.fillRect(0, 0, w, h);
    const s = Number($('scale').value) / 100;
    const { naturalWidth: iw, naturalHeight: ih } = src.img;
    const k = Math.min((w * s) / iw, (h * s) / ih);
    const dw = iw * k;
    const dh = ih * k;
    const x = (w - dw) / 2 + (Number($('ox').value) / 100) * w;
    const y = (h - dh) / 2 + (Number($('oy').value) / 100) * h;
    ctx.imageSmoothingEnabled = true;
    ctx.imageSmoothingQuality = 'high';
    ctx.drawImage(src.img, x, y, dw, dh);
  }
  canvas.style.width = `${(canvas.width / PREVIEW_SCREEN_WIDTH) * 100}%`;
  $('preview-caption').textContent = `${canvas.width}×${canvas.height} px, real size on a 1920×1080 screen`;
  invalidate();
  updateWarnings();
}

// ---------- step 3: build ----------

function imageBytes() {
  return mode() === 'asis' ? state.source.bytes : canvasToBMP($('canvas'));
}

function updateWarnings() {
  const warnings = mode() === 'asis' ? state.source?.info?.warnings || [] : [];
  const ul = $('img-warnings');
  ul.replaceChildren(...warnings.map((w) => el('li', { textContent: w })));
  ul.hidden = warnings.length === 0;
  $('force-label').hidden = warnings.length === 0;
  $('build').disabled = !state.fw || !state.source || (warnings.length > 0 && !$('force').checked);
}

// invalidate drops the patched file once the inputs change.
function invalidate() {
  $('result').hidden = true;
  state.built = false;
  emit('patched', null);
  setError($('build-error'));
  updateTabs();
}

async function build() {
  if ($('build').disabled) return;
  setError($('build-error'));
  const btn = $('build');
  btn.disabled = true;
  btn.firstElementChild.textContent = 'Working…';
  await new Promise((r) => setTimeout(r, 20)); // let the button repaint
  try {
    // replaceLogo verifies the result, including a fresh parse that must read back the image.
    const res = biosimage.replaceLogo(state.fw, imageBytes());
    if (res.error) throw new Error(res.error);
    showResult(res);
  } catch (err) {
    setError($('build-error'), `Could not patch the BIOS: ${err.message}`);
  } finally {
    btn.firstElementChild.textContent = 'Create Patched BIOS';
    updateWarnings();
  }
}

function showResult(res) {
  const c = res.changes[0];
  const capacity = state.fwInfo.logos[0].capacity;
  $('usage-bar').style.width = `${Math.min(100, (c.newSize / capacity) * 100).toFixed(1)}%`;
  $('usage-size').textContent = `${fmtNum(c.newSize)} / ${fmtNum(capacity)} bytes`;
  $('usage-text').textContent = [`${hex8(c.newOffset)} · ${c.storage}, ${c.placement}`,
    ...(res.changes.length > 1 ? [`${res.changes.length} copies`] : []), ...c.recompressed].join(' · ');

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
  emit('patched', res.data);
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
initExplorer();
setupDrop($('fw-drop'), $('fw-input'), loadFirmware);
setupDrop($('img-drop'), $('img-input'), loadImage);
for (const id of ['cw', 'ch', 'bg', 'scale', 'ox', 'oy']) $(id).addEventListener('input', render);
for (const r of document.querySelectorAll('input[name=mode]')) r.addEventListener('change', render);
$('size-reset').addEventListener('click', () => {
  resetSize();
  render();
});
$('center').addEventListener('click', () => {
  $('ox').value = 0;
  $('oy').value = 0;
  render();
});
$('force').addEventListener('change', updateWarnings);
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
updateControls();
updateTabs();
showHelp(DEFAULT_HELP);
