// biosimage web app. Everything runs locally: the firmware parser and patcher are the Go
// package compiled to WebAssembly (cmd/wasm), image conversion uses the browser's canvas.

const $ = (id) => document.getElementById(id);

// Firmware draws the logo unscaled; the preview shows it on a screen of this width.
const PREVIEW_SCREEN_WIDTH = 1920;
const MAX_W = 800;
const MAX_H = 600;
const DEFAULT_SIZE = [672, 378];
const MIME = { BMP: 'image/bmp', PNG: 'image/png', JPEG: 'image/jpeg', GIF: 'image/gif' };
const HEX_BYTES = 64;

const DEFAULT_HELP =
  'Replace the boot logo in an AMI Aptio / UEFI BIOS file.\n\n' +
  '1. Load the BIOS file from your board vendor.\n' +
  '2. Choose and place the new logo.\n' +
  '3. Create, verify and download the patched BIOS.\n\n' +
  'Everything happens in this browser tab. Nothing is uploaded.';

const state = {
  fw: null, // Uint8Array
  fwName: '',
  fwInfo: null, // result of biosimage.inspectFirmware
  source: null, // { img, bytes, name, info }
  built: false,
  urls: {},
};

// ---------- helpers ----------

const fmtBytes = (n) => {
  const [v, u] = n < 1024 ? [n, 'B'] : n < 1 << 20 ? [n / 1024, 'KiB'] : [n / (1 << 20), 'MiB'];
  return `${Number.isInteger(v) ? v : v.toFixed(1)} ${u}`;
};
const fmtNum = (n) => n.toLocaleString('en-US');
const hex8 = (n) => '0x' + n.toString(16).toUpperCase().padStart(8, '0');
const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, Math.round(Number(v) || 0)));
const el = (tag, props = {}, ...children) => {
  const e = Object.assign(document.createElement(tag), props);
  e.append(...children);
  return e;
};

function setError(node, msg) {
  node.textContent = msg || '';
  node.hidden = !msg;
}

function setLocked(id, locked) {
  const node = $(id);
  node.classList.toggle('locked', locked);
  node.inert = locked;
}

// objectURL replaces the blob URL stored under key, revoking the previous one.
function objectURL(key, data, type) {
  if (state.urls[key]) URL.revokeObjectURL(state.urls[key]);
  state.urls[key] = URL.createObjectURL(data instanceof Blob ? data : new Blob([data], { type }));
  return state.urls[key];
}

function sameBytes(a, b) {
  if (!a || !b || a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

function outputName(name) {
  const dot = name.lastIndexOf('.');
  return dot > 0 ? `${name.slice(0, dot)}-logo${name.slice(dot)}` : `${name}-logo`;
}

// ---------- help column and tabs ----------

// showHelp renders text with blank-line separated paragraphs; lines starting with "1." form a list.
function showHelp(text) {
  const box = $('help-text');
  box.replaceChildren();
  for (const para of text.split('\n\n')) {
    const lines = para.split('\n');
    if (lines.every((l) => /^\d+\. /.test(l))) {
      box.append(el('ol', {}, ...lines.map((l) => el('li', { textContent: l.replace(/^\d+\. /, '') }))));
    } else {
      box.append(el('p', { textContent: para }));
    }
  }
}

function onHelpTarget(e) {
  const t = e.target.closest?.('[data-help]');
  if (t) showHelp(t.dataset.help);
}
document.addEventListener('mouseover', onHelpTarget);
document.addEventListener('focusin', onHelpTarget);

const SCREENS = ['fw', 'img', 'build', 'explore'];

// The highlighted tab is the screen the cursor is on, or else the next step to do.
function updateTabs() {
  const done = { fw: !!state.fw, img: !!state.source && !!state.fw, build: state.built };
  const active = state.screen || (!done.fw ? 'fw' : !done.img ? 'img' : 'build');
  for (const k of SCREENS) {
    $(`tab-${k}`).classList.toggle('done', !!done[k]);
    $(`tab-${k}`).classList.toggle('on', k === active);
  }
}

// ---------- keyboard navigation ----------
//
// Like a BIOS setup: ↑/↓ select an item, ←/→ switch screens (sliders and radio buttons keep
// ←/→ for their value), +/- change a value, Enter selects, Home/End jump to the ends.

const ITEM_SELECTOR = 'label.drop, input:not([type=hidden]):not([type=file]), button, a.selbar, .nc-list';

function items(root = document.querySelector('.main')) {
  return [...root.querySelectorAll(ITEM_SELECTOR)].filter((n) =>
    !n.disabled && !n.closest('[inert], [hidden], fieldset[disabled]') && n.getClientRects().length > 0 &&
    !(n.matches('.opt input') && n.closest('.opt').getClientRects().length === 0));
}

function focusItem(n) {
  if (!n) return;
  n.focus({ preventScroll: true });
  n.scrollIntoView({ block: 'nearest' });
}

function focusScreen(key) {
  const first = items($(`step-${key}`))[0];
  if (first) focusItem(first);
}

function stepValue(input, dir) {
  if (dir > 0) input.stepUp();
  else input.stepDown();
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

document.addEventListener('keydown', (e) => {
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  const t = document.activeElement;
  const list = items();
  const i = list.indexOf(t);
  const isRange = t?.type === 'range';
  const isNumber = t?.type === 'number';
  const isRadio = t?.type === 'radio';
  switch (e.key) {
    case 'ArrowDown':
    case 'ArrowUp': {
      e.preventDefault();
      const next = i < 0 ? 0 : i + (e.key === 'ArrowDown' ? 1 : -1);
      focusItem(list[Math.max(0, Math.min(list.length - 1, next))]);
      return;
    }
    case 'Home':
    case 'End':
      if (isNumber) return; // caret movement
      e.preventDefault();
      focusItem(e.key === 'Home' ? list[0] : list[list.length - 1]);
      return;
    case 'ArrowLeft':
    case 'ArrowRight': {
      if (isRange || isRadio || isNumber) return; // native value / caret handling
      e.preventDefault();
      const open = SCREENS.filter((k) => !$(`step-${k}`).classList.contains('locked'));
      const cur = open.indexOf(t?.closest('.step')?.id.replace('step-', ''));
      const dir = e.key === 'ArrowRight' ? 1 : -1;
      focusScreen(open[Math.max(0, Math.min(open.length - 1, cur < 0 ? 0 : cur + dir))]);
      return;
    }
    case '+':
    case '=': // unshifted "+" key on US layouts
    case '-':
      if (isRange || isNumber) {
        e.preventDefault();
        stepValue(t, e.key === '-' ? -1 : 1);
      }
      return;
    case 'Enter':
      if (t?.type === 'checkbox' || isRadio) {
        e.preventDefault();
        t.click();
      }
  }
});

document.addEventListener('focusin', (e) => {
  const step = e.target.closest?.('.step');
  state.screen = step ? step.id.replace('step-', '') : null;
  updateTabs();
});

for (const k of SCREENS) {
  $(`tab-${k}`).addEventListener('click', (e) => {
    e.preventDefault();
    if ($(`step-${k}`).classList.contains('locked')) {
      $(`step-${k}`).scrollIntoView({ block: 'nearest' });
      return;
    }
    focusScreen(k);
  });
}

// ---------- WebAssembly ----------

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

const ready = loadWasm().catch((err) => {
  setError($('wasm-error'), err.message);
  throw err;
});

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

// ---------- hex view ----------

// hexView renders the first bytes of a logo file (bytes), labelled with offsets starting at
// base: file GUID, file header fields and the first section header are highlighted.
function hexView(container, title, bytes, base) {
  const cls = (i) => (i < 16 ? 'g' : i < 24 ? 'h' : i < 48 ? 's' : '');
  const pre = el('pre');
  const end = Math.min(bytes.length, HEX_BYTES);
  for (let row = 0; row < end; row += 16) {
    pre.append(el('span', { className: 'off', textContent: (base + row).toString(16).toUpperCase().padStart(8, '0') }), '  ');
    let ascii = '';
    for (let i = row; i < row + 16; i++) {
      if (i >= end) {
        pre.append('   ');
        continue;
      }
      const b = bytes[i];
      pre.append(el('span', { className: cls(i), textContent: b.toString(16).toUpperCase().padStart(2, '0') }),
        i === row + 7 ? '  ' : ' ');
      ascii += b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '.';
    }
    pre.append(' ', el('span', { className: 'asc', textContent: ascii }), '\n');
  }
  container.replaceChildren(el('span', { className: 'title', textContent: title }), pre);
}

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
    ncSetFirmware('orig', info.volumes > 0 ? bytes : null);
    ncSetFirmware('patched', null);
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
  ncSetFirmware('orig', bytes);
  ncSetFirmware('patched', null);
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

function storageText(logo) {
  switch (logo.encoding) {
    case 'LZMA-compressed section':
      return `LZMA section → RAW${logo.name ? ` (${logo.name})` : ''}`;
    case 'uncompressed section':
      return `RAW section${logo.name ? ` (${logo.name})` : ''}`;
    case 'raw file':
      return 'RAW file';
    default:
      return logo.detail || logo.encoding;
  }
}

function renderFirmware(file) {
  const info = state.fwInfo;
  const logo = info.logos[0];
  const cap = info.capsule;
  const facts = [
    ['File', `${file.name} · ${fmtNum(file.size)} bytes`],
    ['Format', !cap ? 'Flash image' : cap.headerSize ? `${cap.kind} · ${hex8(cap.headerSize).replace(/^0x0+/, '0x')} header` : `Flash image · ${cap.kind}`],
    ['Signature', cap ? (cap.signed ? 'Signed' : 'None') : 'None'],
    ['Volumes', `${info.volumes} firmware volumes`],
    ['Logo File', logo.guid],
    ['Location', logo.container
      ? `${hex8(logo.fileOffset)} in compressed volume (file ${logo.container.guid.slice(0, 8)} @ ${hex8(logo.container.fileOffset)})`
      : `${hex8(logo.fileOffset)} · FV ${hex8(logo.volumeOffset)}, ${fmtBytes(logo.volumeLength)}` +
        (logo.parent ? ` in file ${logo.parent.guid.slice(0, 8)}` : '')],
    ['Stored As', `${storageText(logo)} · ${fmtNum(logo.fileSize)} bytes`],
  ];
  if (logo.imageInfo) {
    const i = logo.imageInfo;
    facts.push(['Current Logo', `${i.format} ${i.width}×${i.height} · ${i.detail}`]);
  }
  facts.push(['Capacity', `${fmtNum(logo.capacity)} bytes`]);
  if (logo.container) {
    facts.push(['Note', `The logo is inside the compressed main volume (${fmtBytes(logo.container.fileSize)}); ` +
      'patching recompresses it and takes a few seconds.']);
  }
  if (info.logos.length > 1) facts.push(['Copies', `${info.logos.length} (all will be replaced)`]);

  $('fw-facts').replaceChildren(...facts.flatMap(([k, v]) => [el('dt', { textContent: k }), el('dd', { textContent: v })]));

  const img = $('fw-logo');
  if (logo.image && logo.imageInfo) {
    img.src = objectURL('fwLogo', logo.image, MIME[logo.imageInfo.format]);
    img.hidden = false;
    $('fw-logo-caption').textContent = `Current: ${logo.name || 'logo'}`;
  } else {
    img.hidden = true;
    $('fw-logo-caption').textContent = 'The current logo cannot be previewed.';
  }
  hexView($('fw-hex'), `Hex View – logo file @ ${hex8(logo.fileOffset)}${logo.container ? ' (decompressed)' : ''}`,
    logo.header, logo.fileOffset);
  $('fw-info').hidden = false;
}

// ---------- step 2: image ----------

// SVGs without width/height have no intrinsic size; give them one from the viewBox so
// every browser can draw them onto a canvas at high resolution.
function sizeSVG(text) {
  const doc = new DOMParser().parseFromString(text, 'image/svg+xml');
  const svg = doc.documentElement;
  if (svg.nodeName !== 'svg') throw new Error('not an SVG file');
  const vb = (svg.getAttribute('viewBox') || '').split(/[\s,]+/).map(Number);
  const hasSize = /^\d+(\.\d+)?(px)?$/.test(svg.getAttribute('width') || '') &&
    /^\d+(\.\d+)?(px)?$/.test(svg.getAttribute('height') || '');
  if (!hasSize) {
    const [w, h] = vb.length === 4 && vb[2] > 0 && vb[3] > 0 ? [vb[2], vb[3]] : [1, 1];
    const k = 2048 / Math.max(w, h);
    svg.setAttribute('width', String(Math.round(w * k)));
    svg.setAttribute('height', String(Math.round(h * k)));
  }
  return new XMLSerializer().serializeToString(doc);
}

// Uses onload rather than img.decode(), which never settles while the page is not being rendered.
function decodeImage(blob) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => (img.naturalWidth && img.naturalHeight ? resolve(img) : reject(new Error('image has no size')));
    img.onerror = () => reject(new Error('cannot decode image'));
    img.src = objectURL('source', blob);
  });
}

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

function invalidate() {
  $('result').hidden = true;
  state.built = false;
  if (nc.sources.patched || nc.pending.patched) ncSetFirmware('patched', null);
  setError($('build-error'));
  updateTabs();
}

// canvasToBMP encodes the canvas as an uncompressed, bottom-up 24-bit BMP.
function canvasToBMP(canvas) {
  const { width: w, height: h } = canvas;
  const px = canvas.getContext('2d').getImageData(0, 0, w, h).data;
  const stride = (w * 3 + 3) & ~3;
  const size = 54 + stride * h;
  const out = new Uint8Array(size);
  const dv = new DataView(out.buffer);
  out[0] = 0x42; // 'B'
  out[1] = 0x4d; // 'M'
  dv.setUint32(2, size, true);
  dv.setUint32(10, 54, true);
  dv.setUint32(14, 40, true);
  dv.setInt32(18, w, true);
  dv.setInt32(22, h, true);
  dv.setUint16(26, 1, true);
  dv.setUint16(28, 24, true);
  dv.setUint32(34, stride * h, true);
  dv.setInt32(38, 2835, true); // 72 dpi
  dv.setInt32(42, 2835, true);
  for (let y = 0; y < h; y++) {
    const row = 54 + (h - 1 - y) * stride;
    for (let x = 0; x < w; x++) {
      const i = (y * w + x) * 4;
      const o = row + x * 3;
      out[o] = px[i + 2];
      out[o + 1] = px[i + 1];
      out[o + 2] = px[i];
    }
  }
  return out;
}

async function build() {
  if ($('build').disabled) return;
  setError($('build-error'));
  const btn = $('build');
  btn.disabled = true;
  btn.firstElementChild.textContent = 'Working…';
  await new Promise((r) => setTimeout(r, 20)); // let the button repaint
  try {
    const img = imageBytes();
    const res = biosimage.replaceLogo(state.fw, img);
    if (res.error) throw new Error(res.error);
    // Independent check: parse the patched file from scratch and compare the logo.
    const check = biosimage.inspectFirmware(res.data);
    if (check.error || !check.logos.every((l) => sameBytes(l.image, img))) {
      throw new Error(`verification of the patched file failed${check.error ? `: ${check.error}` : ''}`);
    }
    showResult(res, check);
  } catch (err) {
    setError($('build-error'), `Could not patch the BIOS: ${err.message}`);
  } finally {
    btn.firstElementChild.textContent = 'Create Patched BIOS';
    updateWarnings();
  }
}

function showResult(res, check) {
  const c = res.changes[0];
  const capacity = state.fwInfo.logos[0].capacity;
  $('usage-bar').style.width = `${Math.min(100, (c.newSize / capacity) * 100).toFixed(1)}%`;
  $('usage-size').textContent = `${fmtNum(c.newSize)} / ${fmtNum(capacity)} bytes`;
  $('usage-text').textContent = `${hex8(c.newOffset)} · ${c.storage}, ${c.placement}` +
    (res.changes.length > 1 ? ` · ${res.changes.length} copies` : '') + (c.container ? ` · ${c.container}` : '');

  const back = check.logos[0];
  $('readback').src = objectURL('readback', back.image, MIME[back.imageInfo?.format] || 'image/bmp');
  hexView($('new-hex'), `Hex View – new logo file @ ${hex8(back.fileOffset)}${back.container ? ' (decompressed)' : ''}`,
    back.header, back.fileOffset);

  const a = $('download');
  const name = outputName(state.fwName);
  a.href = objectURL('download', res.data, 'application/octet-stream');
  a.download = name;
  $('download-name').textContent = `${name} · ${fmtBytes(res.data.length)}`;

  renderFlashHelp();
  $('result').hidden = false;
  ncSetFirmware('patched', res.data);
  state.built = true;
  updateTabs();
  $('result').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

function renderFlashHelp() {
  const cap = state.fwInfo.capsule;
  const asus = cap?.kind === 'AMI Aptio capsule' && /\.cap$/i.test(state.fwName);
  const intro = cap?.signed
    ? 'The original file is signed. The patched copy will not pass the signature check of the BIOS\'s built-in ' +
      'updater (ASUS EZ Flash, MSI M-Flash, Gigabyte Q-Flash, ASRock Instant Flash). Use a method that skips ' +
      'verification, such as ASUS USB BIOS FlashBack, MSI Flash BIOS Button or Gigabyte Q-Flash Plus, and check ' +
      'your board manual for how to use it.'
    : 'Flash the file with your usual method and keep the original at hand.';
  const box = $('flash-help');
  box.replaceChildren(el('p', { textContent: intro }));
  let help = intro;
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

// ---------- firmware explorer (Norton Commander style, read-only) ----------

const HEX_ROWS = 16;

// charsThatFit returns how many monospace characters fit into an element's content width.
function charsThatFit(node) {
  const probe = el('span', { textContent: 'M'.repeat(20) });
  probe.style.cssText = 'position:absolute;visibility:hidden;white-space:pre';
  node.append(probe);
  const w = probe.getBoundingClientRect().width / 20;
  probe.remove();
  const style = getComputedStyle(node);
  const inner = node.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
  return w > 0 ? Math.floor(inner / w) : 80;
}
const nc = {
  sources: { orig: null, patched: null }, // { id, nodes: Map, root }
  pending: { orig: null, patched: null }, // bytes not yet parsed (parsing waits until the explorer is used)
  src: 'orig',
  cwd: null,
  sel: 0,
  rows: [],
  hexPos: 0,
  hexPage: 256,
};

function ncSource() {
  return nc.sources[nc.src];
}

function ncIndex(src, node, parentId) {
  node.parentId = parentId;
  src.nodes.set(node.id, node);
  for (const c of node.children) ncIndex(src, c, node.id);
}

function ncClose(which) {
  if (nc.sources[which]) biosimage.closeExplorer(nc.sources[which].id);
  nc.sources[which] = null;
  nc.pending[which] = null;
}

// ncSetFirmware is called when a BIOS is loaded (or patched): parsing is deferred until the
// explorer is actually looked at.
function ncSetFirmware(which, bytes) {
  ncClose(which);
  nc.pending[which] = bytes;
  if (which === 'patched') {
    $('nc-src-patched').disabled = !bytes;
    if (!bytes && nc.src === 'patched') {
      document.querySelector('input[name=nc-src][value=orig]').checked = true;
      nc.src = 'orig';
    }
  }
  if (nc.src === which) {
    nc.cwd = null;
    if (ncVisible) ncEnsure();
  }
}

function ncEnsure() {
  if (!ncSource() && nc.pending[nc.src]) {
    $('nc-status').textContent = 'Reading firmware structure…';
    const res = biosimage.explore(nc.pending[nc.src]);
    const src = { id: res.id, nodes: new Map(), root: res.root };
    ncIndex(src, res.root, undefined);
    nc.sources[nc.src] = src;
    nc.pending[nc.src] = null;
    nc.cwd = null;
  }
  const src = ncSource();
  if (src && !nc.cwd) {
    nc.cwd = src.root;
    nc.sel = 0;
    ncRender();
  }
}

function ncSize(n) {
  return n < 10000 ? String(n) : n < 10 << 20 ? `${(n / 1024).toFixed(n < 100 << 10 ? 1 : 0)}K` : `${(n / (1 << 20)).toFixed(1)}M`;
}

function ncIsDir(n) {
  return n.lazy || n.children.length > 0;
}

function ncChecksumMark(n) {
  if (n.kind !== 'file') return '';
  return n.info.some(([, v]) => v === 'INVALID') ? ' ✗' : ' ✓';
}

function ncRender() {
  const src = ncSource();
  const dir = nc.cwd;
  if (!src || !dir) return;
  const up = dir.parentId !== undefined ? [{ up: true, node: src.nodes.get(dir.parentId) }] : [];
  nc.rows = up.concat(dir.children.map((node) => ({ node })));
  $('nc-list').replaceChildren(...nc.rows.map((r, i) => {
    const n = r.node;
    const dirRow = r.up || ncIsDir(n);
    const name = r.up ? '..' : `${n.logo ? '★ ' : dirRow ? '▸ ' : '  '}${n.name}`;
    const row = el('div', { className: 'nc-row', role: 'option' },
      el('span', { textContent: name }),
      el('span', { textContent: r.up ? '' : ncSize(n.size) }),
      el('span', { textContent: r.up ? 'UP--DIR' : (n.lazy ? 'compressed' : n.type) + ncChecksumMark(n) }));
    row.classList.toggle('dir', dirRow);
    row.classList.toggle('haslogo', !r.up && (n.hasLogo || n.logo));
    row.addEventListener('click', () => ncSelect(i));
    row.addEventListener('dblclick', () => ncOpen(i));
    return row;
  }));
  const path = [];
  for (let n = dir; n; n = n.parentId !== undefined ? src.nodes.get(n.parentId) : null) path.unshift(n.parentId === undefined ? '' : n.name);
  let title = (path.join('\\') || '\\') + (nc.src === 'patched' ? '  [patched]' : '');
  const fit = charsThatFit($('nc-path').parentElement) - 4;
  if (title.length > fit) title = '…' + title.slice(title.length - fit + 1);
  $('nc-path').textContent = title;
  ncSelect(Math.min(nc.sel, nc.rows.length - 1));
}

function ncSelect(i) {
  nc.sel = Math.max(0, Math.min(nc.rows.length - 1, i));
  const rows = $('nc-list').children;
  for (let k = 0; k < rows.length; k++) rows[k].classList.toggle('sel', k === nc.sel);
  rows[nc.sel]?.scrollIntoView({ block: 'nearest' });
  const r = nc.rows[nc.sel];
  if (!r) return;
  const n = r.up ? nc.cwd : r.node;
  nc.hexPos = 0;
  ncInfo(n);
  ncHex(n);
}

function ncInfo(n) {
  const where = n.buffer ? ' (in decompressed data)' : '';
  const facts = [['Kind', n.kind], ['Offset', hex8(n.offset) + where], ['Size', `${fmtNum(n.size)} bytes`]];
  if (n.guid) facts.push(['GUID', n.guid]);
  for (const [k, v] of n.info) if (k !== 'GUID') facts.push([k, v]);
  if (n.logo) facts.push(['Boot logo', 'this is the file biosimage replaces']);
  else if (n.hasLogo) facts.push(['Boot logo', 'inside']);
  if (n.lazy) facts.push(['Open', 'press Enter to decompress']);
  $('nc-info-title').textContent = n.name;
  $('nc-info').replaceChildren(...facts.flatMap(([k, v]) => [el('dt', { textContent: k }), el('dd', { textContent: v })]));
  $('nc-status').textContent = `${hex8(n.offset)}${where} · ${fmtNum(n.size)} bytes · ${n.type || n.kind}`;
  const prev = $('nc-preview');
  prev.hidden = !n.image;
  if (n.image) {
    const res = biosimage.nodeBytes(ncSource().id, n.id, n.imageOffset, n.size - n.imageOffset);
    $('nc-img').src = objectURL('ncImage', res.data, MIME[n.image] || 'application/octet-stream');
  }
}

function ncHex(n) {
  const pre = $('nc-hex');
  // 16 bytes per row (76 characters) when the panel is wide enough, else 8 (43 characters);
  // a page is always HEX_ROWS rows.
  const fit = charsThatFit(pre);
  const per = fit >= 76 ? 16 : 8;
  const showAscii = fit >= 43;
  nc.hexPage = per * HEX_ROWS;
  nc.hexPos -= nc.hexPos % nc.hexPage;
  const res = biosimage.nodeBytes(ncSource().id, n.id, nc.hexPos, nc.hexPage);
  const data = res.data || new Uint8Array();
  pre.replaceChildren();
  for (let row = 0; row < data.length; row += per) {
    const chunk = data.subarray(row, row + per);
    const hexs = [...chunk].map((b) => b.toString(16).toUpperCase().padStart(2, '0'));
    const ascii = [...chunk].map((b) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '.')).join('');
    const groups = per === 16 ? `${hexs.slice(0, 8).join(' ').padEnd(23)}  ${hexs.slice(8).join(' ').padEnd(23)}` : hexs.join(' ').padEnd(23);
    pre.append(el('span', { className: 'off', textContent: (n.offset + nc.hexPos + row).toString(16).toUpperCase().padStart(8, '0') }),
      `  ${groups}  `, el('span', { className: 'asc', textContent: showAscii ? ascii : '' }), '\n');
  }
  $('nc-hex-title').textContent = `Hex${n.buffer ? ' (decompressed)' : ''}`;
  $('nc-hex-pos').textContent = n.size
    ? `${fmtNum(nc.hexPos)}–${fmtNum(nc.hexPos + data.length)} of ${fmtNum(n.size)}`
    : '';
}

function ncHexPage(dir) {
  const r = nc.rows[nc.sel];
  if (!r) return;
  const n = r.up ? nc.cwd : r.node;
  const pos = nc.hexPos + dir * nc.hexPage;
  if (pos < 0 || pos >= n.size) return;
  nc.hexPos = pos;
  ncHex(n);
}

// ncExpand fetches the children of a node that is still lazy on the JavaScript side.
function ncExpand(n) {
  const src = ncSource();
  $('nc-status').textContent = 'Decompressing…';
  const res = biosimage.expandNode(src.id, n.id);
  if (res.error) {
    $('nc-status').textContent = `Error: ${res.error}`;
    return false;
  }
  n.children = res.children;
  n.lazy = false;
  for (const c of n.children) ncIndex(src, c, n.id);
  return true;
}

// repaint lets the browser show a status message before a long synchronous call.
const repaint = () => new Promise((r) => setTimeout(r, 30));

async function ncOpen(i) {
  const r = nc.rows[i];
  if (!r) return;
  if (r.up) return ncUp();
  const n = r.node;
  if (n.lazy) {
    $('nc-status').textContent = 'Decompressing…';
    await repaint();
    if (!ncExpand(n)) return;
  }
  if (!n.children.length) return;
  nc.cwd = n;
  nc.sel = 1;
  ncRender();
}

function ncUp() {
  const src = ncSource();
  if (!src || nc.cwd.parentId === undefined) return;
  const prev = nc.cwd;
  nc.cwd = src.nodes.get(prev.parentId);
  nc.sel = nc.cwd.children.indexOf(prev) + (nc.cwd.parentId !== undefined ? 1 : 0);
  ncRender();
}

async function ncGoLogo() {
  ncEnsure();
  const src = ncSource();
  if (!src) return;
  $('nc-status').textContent = 'Searching for the boot logo…';
  await repaint();
  const path = biosimage.logoPath(src.id).path || [];
  if (!path.length) {
    $('nc-status').textContent = 'No boot logo file found in this image.';
    return;
  }
  let dir = src.root;
  for (const id of path.slice(1)) {
    let next = dir.children.find((c) => c.id === id);
    if (!next && (dir.lazy || dir.children.length === 0 || !src.nodes.has(id))) {
      if (!ncExpand(dir)) return;
      next = dir.children.find((c) => c.id === id);
    }
    if (!next) return;
    if (id === path[path.length - 1]) {
      nc.cwd = dir;
      nc.sel = dir.children.indexOf(next) + (dir.parentId !== undefined ? 1 : 0);
      ncRender();
      $('nc-list').focus({ preventScroll: true });
      return;
    }
    dir = next;
  }
}

$('nc-list').addEventListener('keydown', (e) => {
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  const last = nc.rows.length - 1;
  const actions = {
    ArrowDown: () => nc.sel < last && ncSelect(nc.sel + 1),
    ArrowUp: () => nc.sel > 0 && ncSelect(nc.sel - 1),
    PageDown: () => ncSelect(nc.sel + 15),
    PageUp: () => ncSelect(nc.sel - 15),
    Home: () => ncSelect(0),
    End: () => ncSelect(last),
    Enter: () => ncOpen(nc.sel),
    ArrowRight: () => ncOpen(nc.sel),
    Backspace: ncUp,
    ArrowLeft: ncUp,
    l: ncGoLogo,
    L: ncGoLogo,
    '[': () => ncHexPage(-1),
    ']': () => ncHexPage(1),
  };
  // At the ends of the list, ↑/↓ leave the explorer like any other item.
  if ((e.key === 'ArrowDown' && nc.sel >= last) || (e.key === 'ArrowUp' && nc.sel <= 0)) return;
  const action = actions[e.key];
  if (!action) return;
  e.preventDefault();
  e.stopPropagation();
  action();
});
$('nc-list').addEventListener('focus', ncEnsure);
$('nc-list').addEventListener('mousedown', ncEnsure);
$('step-explore').addEventListener('focusin', () => setTimeout(ncEnsure, 0));
$('nc-logo').addEventListener('click', ncGoLogo);
$('nc-hex-prev').addEventListener('click', () => ncHexPage(-1));
$('nc-hex-next').addEventListener('click', () => ncHexPage(1));
for (const r of document.querySelectorAll('input[name=nc-src]')) {
  r.addEventListener('change', () => {
    nc.src = r.value;
    nc.cwd = null;
    ncEnsure();
  });
}

// Parse the structure only once the explorer scrolls into view.
let ncVisible = false;
new IntersectionObserver((entries) => {
  ncVisible = entries.some((en) => en.isIntersecting);
  if (ncVisible && !$('step-explore').classList.contains('locked')) setTimeout(ncEnsure, 0);
}).observe($('step-explore'));

// ---------- wiring ----------

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
