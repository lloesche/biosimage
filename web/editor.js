// The New Logo step: the image, how it is placed on the canvas, the output format and the
// encoded output. The output is encoded once per change and is both what the preview shows and
// what gets patched in. Changes are reported with the logo event, changed warnings (which can
// block patching) with the warnings event.

import { $, MIME, clamp, el, fmtNum, objectURL, setError, setupDrop } from './ui.js';
import { decodeImage, sizeSVG } from './image.js';
import { emit, state } from './state.js';
import { call } from './wasm.js';

// Firmware draws the logo unscaled; the preview shows it on a screen of this width.
const PREVIEW_SCREEN_WIDTH = 1920;
const MAX_W = 800;
const MAX_H = 600;
const DEFAULT_SIZE = [672, 378];
// Formats the canvas can be encoded in; GIF logos can only be inserted as-is.
const ENCODABLE = ['BMP', 'JPEG', 'PNG'];

const ed = {
  version: 0, // counts changes to the output
  output: null, // { version, bytes: Promise<Uint8Array> }
  warnings: [],
  timer: 0,
};

// mode is the output format ("BMP", "JPEG", "PNG") or "asis".
function mode() {
  return document.querySelector('input[name=mode]:checked')?.value || 'BMP';
}

// output returns the logo to patch in for the current settings, encoding it on first use.
export function output() {
  if (ed.output?.version !== ed.version) ed.output = { version: ed.version, bytes: encode() };
  return ed.output.bytes;
}

async function encode() {
  const m = mode();
  if (m === 'asis') return state.source.bytes;
  const canvas = $('canvas');
  const px = canvas.getContext('2d').getImageData(0, 0, canvas.width, canvas.height).data;
  const res = await call('encodeImage', [px, canvas.width, canvas.height, m, Number($('quality').value)]);
  if (res.error) throw new Error(res.error);
  return res.data;
}

// blocked reports whether there are image warnings that have not been overridden.
export function blocked() {
  return ed.warnings.length > 0 && !$('force').checked;
}

// ---------- firmware and formats ----------

// setFirmware adapts the editor to a newly loaded BIOS: the canvas size of its logo and the
// formats known without looking for decoders (state.fwInfo.formats).
export function setFirmware() {
  resetSize();
  showFormats(state.fwInfo.formats, false);
  inspectSource();
  render();
}

// setDetectedFormats offers the formats found by looking for decoders, keeping the chosen one.
export function setDetectedFormats(formats) {
  const before = mode();
  showFormats(formats, true);
  if (state.source && mode() !== before) render();
  inspectSource();
}

// showFormats offers formats as output. Once they are detected, the chosen one is kept if it is
// still offered; otherwise it starts with the format of the current logo.
function showFormats(formats, detected) {
  const before = mode();
  const encodable = formats.filter((f) => ENCODABLE.includes(f.format));
  const prev = detected && before;
  const def = (encodable.some((f) => f.format === prev) && prev) || encodable.find((f) => f.current)?.format || 'BMP';
  const asis = detected && before === 'asis';
  state.formats = formats;
  $('formats').replaceChildren(...encodable.flatMap((f) => [
    el('label', { className: 'opt' }, el('input', { type: 'radio', name: 'mode', value: f.format, checked: !asis && f.format === def }),
      el('span', { textContent: f.format })), ' ']));
  if (asis) document.querySelector('input[name=mode][value=asis]').checked = true;
  $('formats-hint').textContent = 'This BIOS can show ' + formats.map((f) =>
    `${f.format} (${f.reason}${ENCODABLE.includes(f.format) ? '' : ', insert as-is'})`).join(', ') +
    (detected ? '.' : '; looking for more decoders…');
}

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
  setError($('img-error'));
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
  const info = isSVG ? null : await call('inspectImage', [bytes, state.fwId]);
  const si = info && !info.error ? info : null;
  state.source = { img, bytes, name: file.name, info: si, decodable: !!si };
  $('img-drop-main').textContent = `[ ${file.name} ]`;

  // "File as-is" only for formats the firmware can decode.
  const asis = document.querySelector('input[name=mode][value=asis]');
  asis.disabled = !si;
  if (asis.disabled && asis.checked) document.querySelector('#formats input').checked = true;
  $('mode-hint').textContent = !si
    ? `${file.name}: converted to BMP.`
    : si.format === 'GIF' && !si.detail.startsWith('1 ')
      ? `Animated GIF (${si.detail}): choose “File as-is” to keep the animation; conversion uses the first frame.`
      : `${file.name}: ${si.format} ${si.width}×${si.height}, ${si.detail}, ${fmtNum(bytes.length)} bytes.`;
  $('editor').hidden = false;
  render();
}

function resetSize() {
  const i = state.fwInfo?.logos[0]?.imageInfo;
  const [w, h] = i ? [i.width, i.height] : DEFAULT_SIZE;
  $('cw').value = clamp(w, 16, MAX_W);
  $('ch').value = clamp(h, 16, MAX_H);
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
  for (const id of ['ox', 'oy']) {
    const v = Number($(id).value);
    $(`${id}-out`).textContent = v === 0 ? 'Centre' : `${v > 0 ? '+' : ''}${v}%`;
  }
  $('bg-hex').textContent = $('bg').value.toUpperCase();
}

// render draws the canvas, the logo before encoding.
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
  $('encoded').hidden = true; // until the new canvas is encoded
  updateWarnings();
  changed();
}

// changed starts over with a new output.
function changed() {
  ed.version++;
  emit('logo');
  showOutput();
}

// showOutput shows the size of the output once the controls have been still for a moment, and
// for JPEG the encoded file over the canvas, so that the preview has the artefacts of the chosen
// quality; BMP and PNG are lossless.
function showOutput() {
  const canvas = $('canvas');
  const caption = `${canvas.width}×${canvas.height} px, real size on a 1920×1080 screen`;
  $('preview-caption').textContent = caption;
  clearTimeout(ed.timer);
  ed.timer = setTimeout(async () => {
    const m = mode();
    const version = ed.version;
    let text, bytes;
    try {
      bytes = await output();
      text = `${caption} · ${m === 'asis' ? 'file' : m} ${fmtNum(bytes.length)} bytes`;
    } catch (err) {
      text = `${caption} · ${err.message}`;
    }
    if (version !== ed.version) return;
    $('preview-caption').textContent = text;
    const enc = $('encoded');
    enc.hidden = !bytes || m !== 'JPEG';
    if (!enc.hidden) {
      enc.src = objectURL('encoded', bytes, MIME.JPEG);
      enc.style.width = canvas.style.width;
    }
  }, 150);
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
  $('editor').addEventListener('change', (e) => e.target.name === 'mode' && render());
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
  updateControls();
}
