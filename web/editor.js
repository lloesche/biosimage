// The New Logo step: the image, how it is placed on the canvas, the output format and the
// encoded output. The output is encoded once per change; it is what the preview shows, what is
// checked against the space in the BIOS and what gets patched in. Changes are reported with the
// logo event; changes to what can block patching (image warnings, a logo that does not fit) with
// the warnings event.

import { $, MIME, clamp, el, fmtNum, objectURL, setError, setupDrop } from './ui.js';
import { decodeImage, sizeSVG } from './image.js';
import { emit, state } from './state.js';
import { call } from './wasm.js';

// Firmware draws the logo unscaled; the preview shows it on a screen of this width.
const PREVIEW_SCREEN_WIDTH = 1920;
// The largest logo: the firmware shows none larger than the screen (uefi.InspectImage).
const MAX_W = 1920;
const MAX_H = 1080;
const DEFAULT_SIZE = [672, 378];

const ed = {
  version: 0, // counts changes to the output
  output: null, // { version, encoded: Promise<{ data, preview }> }
  fit: null, // fitLogo result for the output, once known
  bootGuard: '', // the Intel Boot Guard warning for patching the output, if any
  warnings: [],
  loads: 0, // to drop an image that has been replaced by another one while loading
  timer: 0,
};

// mode is the output format ("BMP", "JPEG", "PNG", "GIF") or "asis".
function mode() {
  return document.querySelector('input[name=mode]:checked')?.value || 'BMP';
}

const outputFormat = () => (mode() === 'asis' ? state.source.info.format : mode());

// animated reports whether the output is an animation.
const animated = () => mode() === 'GIF' && !!state.source?.anim;

// shownFrames returns the frames of the animation to store, [{index, delay}], for the timing.
const shownFrames = () => state.source.anim.timing[document.querySelector('input[name=timing]:checked').value];

// encoded returns the logo to patch in for the current settings, encoding it on first use:
// data, and preview for the browser (the same except for an animation's frame timing, see
// uefi.FirmwareTiming).
function encoded() {
  if (ed.output?.version !== ed.version) ed.output = { version: ed.version, encoded: encode() };
  return ed.output.encoded;
}

// output returns the logo to patch in.
export const output = async () => (await encoded()).data;

// storeOptions returns how replaceLogo is to store the logo.
export const storeOptions = () => ({
  compress: !$('compress-label').hidden && $('compress').checked,
  freeSpace: !$('free-label').hidden && $('free-space').checked,
});

async function encode() {
  const m = mode();
  const src = state.source;
  if (m === 'asis') return { data: src.bytes, preview: src.bytes };
  const { width: w, height: h } = $('canvas');
  let res;
  if (animated()) {
    const ctx = Object.assign(document.createElement('canvas'), { width: w, height: h }).getContext('2d', { willReadFrequently: true });
    const shown = shownFrames();
    const frames = shown.map((f) => {
      place(ctx, src.anim.frames[f.index]);
      return ctx.getImageData(0, 0, w, h).data;
    });
    const full = document.querySelector('input[name=store]:checked').value === 'full';
    res = await call('encodeAnimation', [frames, w, h, shown.map((f) => f.delay), full]);
  } else {
    const px = $('canvas').getContext('2d').getImageData(0, 0, w, h).data;
    res = await call('encodeImage', [px, w, h, m, Number($('quality').value)]);
  }
  if (res.error) throw new Error(res.error);
  return { data: res.data, preview: res.preview || res.data };
}

// blocked reports whether the output does not fit, or has image or Boot Guard warnings that
// have not been confirmed.
export function blocked() {
  return (ed.fit && !ed.fit.fits) || (ed.warnings.length > 0 && !$('force').checked) ||
    (!!ed.bootGuard && !$('bootguard-ok').checked);
}

// ---------- firmware and formats ----------

// setFirmware adapts the editor to a newly loaded BIOS: the canvas size of its logo and the
// formats known without reading the logo code (state.fwInfo.formats).
export function setFirmware() {
  const logo = state.fwInfo.logos[0];
  $('compress').checked = logo.lzma; // like the original
  $('compress-label').hidden = logo.rawFile; // a raw file cannot hold a compressed image
  $('bootguard-ok').checked = false;
  setFreeSpace(0);
  resetSize();
  showFormats(state.fwInfo.formats, false);
  inspectSource();
  render();
}

// setDetectedFormats offers the formats the logo code reads, keeping the chosen one.
export function setDetectedFormats(formats) {
  const before = mode();
  showFormats(formats, true);
  if (state.source && mode() !== before) render();
  inspectSource();
}

// setFreeSpace offers to move the logo into free space of this many bytes (none if 0), found
// with the formats.
export function setFreeSpace(capacity) {
  $('free-label').hidden = !capacity;
  if (!capacity) $('free-space').checked = false;
  if (state.source) refit();
}

// refit checks the output against the space again, after the place to store it changed.
function refit() {
  ed.fit = null;
  emit('logo');
  showOutput();
}

// showFormats offers formats as output. Once they are detected, the chosen one is kept if it is
// still offered; otherwise it starts with the format of the current logo.
function showFormats(formats, detected) {
  const before = mode();
  const prev = detected && before;
  const def = (formats.some((f) => f.format === prev) && prev) || formats.find((f) => f.current)?.format || 'BMP';
  const asis = detected && before === 'asis';
  state.formats = formats;
  $('formats').replaceChildren(...formats.flatMap((f) => [
    el('label', { className: 'opt' }, el('input', { type: 'radio', name: 'mode', value: f.format, checked: !asis && f.format === def }),
      el('span', { textContent: f.format })), ' ']));
  if (asis) document.querySelector('input[name=mode][value=asis]').checked = true;
  $('formats-hint').textContent = 'This BIOS can show ' + formats.map((f) => `${f.format} (${f.reason})`).join(', ') +
    (detected ? '.' : '; checking which formats its logo code reads…');
}

const offered = (format) => document.querySelector(`#formats input[value=${format}]`);

// inspectSource checks the new image against the BIOS: its warnings depend on the formats.
async function inspectSource() {
  const src = state.source;
  if (!src?.decodable || !state.fwId) return;
  const info = await call('inspectImage', [src.bytes, state.fwId]);
  if (state.source !== src) return;
  src.info = info.error ? null : info;
  updateWarnings();
}

// ---------- image ----------

async function loadImage(file) {
  const load = ++ed.loads;
  setError($('img-error'));
  const bytes = new Uint8Array(await file.arrayBuffer());
  const isSVG = file.type === 'image/svg+xml' || /\.svg$/i.test(file.name);
  let img;
  try {
    const blob = isSVG ? new Blob([sizeSVG(new TextDecoder().decode(bytes))], { type: 'image/svg+xml' }) : file;
    img = await decodeImage(blob);
  } catch {
    if (load === ed.loads) setError($('img-error'), `${file.name}: your browser cannot read this image.`);
    return;
  }
  const info = isSVG ? null : await call('inspectImage', [bytes, state.fwId]);
  const si = info && !info.error ? info : null;
  const src = { img, bytes, name: file.name, info: si, decodable: !!si };
  if (si?.frames > 1) await loadAnimation(src, file.name);
  if (load !== ed.loads) return;
  state.source = src;
  $('img-drop-main').textContent = `[ ${file.name} ]`;

  // "File as-is" only for formats the firmware can decode.
  const asis = document.querySelector('input[name=mode][value=asis]');
  asis.disabled = !si;
  if (asis.disabled && asis.checked) document.querySelector('#formats input').checked = true;
  if (src.anim && offered('GIF')) offered('GIF').checked = true;
  $('editor').hidden = false;
  render();
}

// loadAnimation adds the frames of an animated GIF to src, and the frames AMI firmware is to
// show for each timing.
async function loadAnimation(src, name) {
  const res = await call('decodeAnimation', [src.bytes], `Reading ${name}`);
  if (res.error) {
    src.animError = res.error;
    return;
  }
  const frames = await Promise.all(res.frames.map((px) =>
    createImageBitmap(new ImageData(new Uint8ClampedArray(px.buffer, px.byteOffset, px.length), res.width, res.height))));
  src.anim = { frames, delays: res.delays, timing: res.timing };
}

// resetSize sets the canvas to the size of the current logo, with the logo at the default size
// in the centre.
function resetSize() {
  const i = state.fwInfo?.logos[0]?.imageInfo;
  const [w, h] = i ? [i.width, i.height] : DEFAULT_SIZE;
  $('cw').value = clamp(w, 16, MAX_W);
  $('ch').value = clamp(h, 16, MAX_H);
  $('scale').value = $('scale').defaultValue;
  $('ox').value = 0;
  $('oy').value = 0;
}

// sourceSize sizes the canvas to the new image (scaled down to the maximum) and draws it 1:1.
function sourceSize() {
  const { naturalWidth: w, naturalHeight: h } = state.source.img;
  const k = Math.min(1, MAX_W / w, MAX_H / h);
  $('cw').value = clamp(w * k, 16, MAX_W);
  $('ch').value = clamp(h * k, 16, MAX_H);
  $('scale').value = 100;
  $('ox').value = 0;
  $('oy').value = 0;
}

function updateControls() {
  for (const r of [$('scale'), $('quality')]) r.style.setProperty('--p', `${((r.value - r.min) / (r.max - r.min)) * 100}%`);
  $('scale-out').textContent = `${$('scale').value}%`;
  $('quality-out').textContent = $('quality').value;
  $('quality-field').hidden = mode() !== 'JPEG';
  $('timing-field').hidden = $('frames-field').hidden = !animated();
  for (const id of ['ox', 'oy']) {
    const v = Number($(id).value);
    $(`${id}-out`).textContent = v === 0 ? 'Centre' : `${v > 0 ? '+' : ''}${v}%`;
  }
  $('bg-hex').textContent = $('bg').value.toUpperCase();
}

// updateHint describes the new image and, for an animation, what the output keeps of it.
function updateHint() {
  const { name, img, info: si, bytes, anim, animError } = state.source;
  let text = si
    ? `${name}: ${si.format} ${si.width}×${si.height}, ${si.detail}, ${fmtNum(bytes.length)} bytes.`
    : `${name}: ${img.naturalWidth}×${img.naturalHeight}.`;
  const m = mode();
  if (si?.frames > 1 && m !== 'asis') {
    text += animError ? ` Its animation could not be read (${animError}); only the first frame is used.`
      : m === 'GIF' ? ` Animated: ${animationSummary()}. It plays while the board starts up (POST), usually a few seconds; some boards show only the first frame.`
      : ` ${m} keeps only the first frame${offered('GIF') ? '; choose GIF to keep the animation' : ''}.`;
  }
  $('mode-hint').textContent = text;
}

// animationSummary says how many frames are stored and how long a loop takes.
function animationSummary() {
  const { frames, delays } = state.source.anim;
  const shown = shownFrames();
  const secs = (cs) => `${+(cs / 100).toFixed(2)} s`;
  const sum = (list) => list.reduce((a, b) => a + b, 0);
  const loop = sum(shown.map((f) => f.delay));
  const orig = sum(delays);
  return `${shown.length === frames.length ? `all ${frames.length}` : `${shown.length} of ${frames.length}`} frames, ` +
    `${secs(loop)} per loop${loop === orig ? '' : ` (originally ${secs(orig)})`}`;
}

// place draws image onto ctx as the layout controls say: scaled and moved on the background.
function place(ctx, image) {
  const { width: w, height: h } = ctx.canvas;
  ctx.fillStyle = $('bg').value;
  ctx.fillRect(0, 0, w, h);
  const s = Number($('scale').value) / 100;
  const { naturalWidth: iw, naturalHeight: ih } = state.source.img;
  const k = Math.min((w * s) / iw, (h * s) / ih);
  const dw = iw * k;
  const dh = ih * k;
  const x = (w - dw) / 2 + (Number($('ox').value) / 100) * w;
  const y = (h - dh) / 2 + (Number($('oy').value) / 100) * h;
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = 'high';
  ctx.drawImage(image, x, y, dw, dh);
}

// render draws the canvas, the logo (the first frame of an animation) before encoding.
function render() {
  updateControls();
  const src = state.source;
  if (!src) return;
  updateHint();
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
    canvas.width = clamp($('cw').value, 16, MAX_W);
    canvas.height = clamp($('ch').value, 16, MAX_H);
    place(ctx, src.anim?.frames[0] || src.img);
  }
  canvas.style.width = `${(canvas.width / PREVIEW_SCREEN_WIDTH) * 100}%`;
  $('encoded').hidden = true; // until the new canvas is encoded
  updateWarnings();
  changed();
}

// changed starts over with a new output.
function changed() {
  ed.version++;
  ed.fit = null;
  emit('logo');
  showOutput();
}

// showOutput shows the output once the controls have been still for a moment: the encoded file
// over the canvas (so the preview has the artefacts of JPEG and the colours and animation of
// GIF), its size, and the space it takes in the BIOS.
function showOutput() {
  const canvas = $('canvas');
  const caption = `${canvas.width}×${canvas.height} px, real size on a 1920×1080 screen`;
  $('preview-caption').textContent = caption;
  clearTimeout(ed.timer);
  ed.timer = setTimeout(async () => {
    const version = ed.version;
    const m = mode();
    let bytes, preview, fit;
    try {
      ({ data: bytes, preview } = await encoded());
      fit = await call('fitLogo', [state.fwId, bytes, storeOptions()]);
    } catch (err) {
      if (version === ed.version) $('preview-caption').textContent = `${caption} · ${err.message}`;
      return;
    }
    if (version !== ed.version) return;
    $('preview-caption').textContent = `${caption} · ${m === 'asis' ? 'file' : m} ${fmtNum(bytes.length)} bytes`;
    const enc = $('encoded');
    enc.onload = () => (enc.hidden = version !== ed.version);
    enc.src = objectURL('encoded', preview, MIME[outputFormat()]);
    enc.style.width = canvas.style.width;
    showFit(fit);
  }, 150);
}

// showFit shows how much of the space in the BIOS the output takes, and whether it fits.
function showFit(fit) {
  ed.fit = fit.error && !fit.size ? null : fit; // e.g. no logo: Build reports it
  $('space-field').hidden = !ed.fit;
  if (ed.fit) {
    const { size, capacity, fits } = fit;
    const bar = $('space-bar');
    bar.style.width = `${Math.min(100, (size / capacity) * 100).toFixed(1)}%`;
    bar.classList.toggle('over', !fits);
    $('space-size').textContent = `${fmtNum(size)} / ${fmtNum(capacity)} bytes · ${fit.storage}`;
  }
  const tip = (!$('compress-label').hidden && !$('compress').checked ? 'Store it compressed. ' : '') +
    ({ BMP: 'Choose JPEG, or make the canvas or the logo smaller.', JPEG: 'Lower the quality, or make the canvas smaller.',
      GIF: 'Make the canvas or the logo smaller, or use an animation with fewer frames.', asis: 'Use a smaller file.' }[mode()] ||
    'Choose JPEG, or make the canvas smaller.');
  setError($('space-error'), !ed.fit || fit.fits ? ''
    : fit.size > fit.capacity ? `The new logo does not fit: its file needs ${fmtNum(fit.size)} bytes and the BIOS has room for ${fmtNum(fit.capacity)}. ${tip}`
    : fit.error);
  ed.bootGuard = fit.bootGuard || '';
  setError($('bootguard-warning'), ed.bootGuard);
  $('bootguard-label').hidden = !ed.bootGuard;
  emit('warnings');
}

function updateWarnings() {
  const si = state.source?.info;
  ed.warnings = mode() === 'asis' && si ? si.warnings : [];
  $('img-warnings').replaceChildren(...ed.warnings.map((w) => el('li', { textContent: w })));
  $('img-warnings').hidden = ed.warnings.length === 0;
  $('force-label').hidden = ed.warnings.length === 0;
  emit('warnings');
}

export function initEditor() {
  setupDrop($('img-drop'), $('img-input'), loadImage);
  for (const id of ['cw', 'ch', 'bg', 'scale', 'ox', 'oy']) $(id).addEventListener('input', render);
  // A new quality keeps the canvas, and the shown JPEG until the new one is ready.
  $('quality').addEventListener('input', () => {
    updateControls();
    changed();
  });
  $('editor').addEventListener('change', (e) => ['mode', 'timing', 'store'].includes(e.target.name) && render());
  $('size-source').addEventListener('click', () => {
    sourceSize();
    render();
  });
  $('size-reset').addEventListener('click', () => {
    resetSize();
    render();
  });
  $('center').addEventListener('click', () => {
    $('ox').value = 0;
    $('oy').value = 0;
    render();
  });
  $('force').addEventListener('change', () => emit('warnings'));
  $('free-space').addEventListener('change', refit);
  $('compress').addEventListener('change', refit);
  $('bootguard-ok').addEventListener('change', () => emit('warnings'));
  updateControls();
}
