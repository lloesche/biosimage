// Firmware explorer (Norton Commander style, read-only): the structure tree on the left, details,
// image preview and hex dump on the right. It shows the images of the firmware and patched
// events (opened in the worker, see app.js) and only fetches a tree once the explorer is
// actually looked at.

import { $, MIME, charsThatFit, el, fmtNum, hex8, objectURL } from './ui.js';
import { hexRows } from './hex.js';
import { events } from './state.js';
import { call } from './wasm.js';

const HEX_ROWS = 16;

const nc = {
  sources: { orig: null, patched: null }, // { id, nodes: Map, root, logoPath }; id is the image
  pending: { orig: 0, patched: 0 }, // images whose tree has not been fetched yet
  loading: null, // { id, promise } of the fetch in progress
  src: 'orig',
  cwd: null,
  sel: 0,
  rows: [],
  hexPos: 0,
  hexPage: 256,
  visible: false,
};

function ncSource() {
  return nc.sources[nc.src];
}

function ncIndex(src, node, parentId) {
  node.parentId = parentId;
  src.nodes.set(node.id, node);
  for (const c of node.children) ncIndex(src, c, node.id);
}

// ncSetImage shows image id (0 for none) as the original or patched firmware.
function ncSetImage(which, id) {
  if (!id && !nc.sources[which] && !nc.pending[which]) return;
  nc.sources[which] = null;
  nc.pending[which] = id;
  if (which === 'patched') {
    $('nc-src-patched').disabled = !id;
    if (!id && nc.src === 'patched') {
      document.querySelector('input[name=nc-src][value=orig]').checked = true;
      nc.src = 'orig';
    }
  }
  if (nc.src === which) {
    nc.cwd = null;
    if (nc.visible) ncEnsure();
  }
}

// ncLoad fetches the tree of image id for the given source.
async function ncLoad(which, id) {
  $('nc-status').textContent = 'Reading firmware structure…';
  const res = await call('explore', [id], 'Reading the firmware structure');
  if (res.error || nc.pending[which] !== id) return; // replaced meanwhile
  const src = { id, nodes: new Map(), root: res.root, logoPath: res.logoPath };
  ncIndex(src, res.root, undefined);
  nc.sources[which] = src;
  nc.pending[which] = 0;
  if (nc.src === which) nc.cwd = null;
}

// ncEnsure fetches the tree being shown, if that has not happened yet, and shows its root.
async function ncEnsure() {
  const which = nc.src;
  const id = nc.pending[which];
  if (id && !nc.sources[which]) {
    if (nc.loading?.id !== id) nc.loading = { id, promise: ncLoad(which, id) };
    await nc.loading.promise;
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
  const n = ncSelected();
  if (!n) return;
  nc.hexPos = 0;
  ncInfo(n);
  ncHex(n);
}

// ncSelected returns the node of the selected row (for "..", the current directory).
function ncSelected() {
  const r = nc.rows[nc.sel];
  return r && (r.up ? nc.cwd : r.node);
}

// ncImageNode returns the node holding the image to preview for n: n itself, or for files and
// sections the first image stored inside them (already decompressed, a few levels deep), so
// that e.g. the logo file shows the logo without opening its sections.
function ncImageNode(n) {
  if (n.image) return n;
  if (n.kind !== 'file' && n.kind !== 'section') return null;
  let level = n.children;
  for (let depth = 0; depth < 4 && level.length; depth++) {
    const found = level.find((c) => c.image);
    if (found) return found;
    level = level.flatMap((c) => c.children);
  }
  return null;
}

async function ncInfo(n) {
  const where = n.decompressed ? ' (in decompressed data)' : '';
  const facts = [['Kind', n.kind], ['Offset', hex8(n.offset) + where], ['Size', `${fmtNum(n.size)} bytes`]];
  if (n.guid) facts.push(['GUID', n.guid]);
  for (const [k, v] of n.info) if (k !== 'GUID') facts.push([k, v]);
  if (n.logo) facts.push(['Boot logo', 'this is the file biosimage replaces']);
  else if (n.hasLogo) facts.push(['Boot logo', 'inside']);
  if (n.lazy) facts.push(['Open', 'press Enter to decompress']);
  const img = ncImageNode(n);
  if (img && img !== n) facts.push(['Contains', img.name.replace(/^Image: /, 'image ')]);
  $('nc-info-title').textContent = n.name;
  $('nc-info').replaceChildren(...facts.flatMap(([k, v]) => [el('dt', { textContent: k }), el('dd', { textContent: v })]));
  $('nc-status').textContent = `${hex8(n.offset)}${where} · ${fmtNum(n.size)} bytes · ${n.type || n.kind}`;
  $('nc-preview').hidden = !img;
  if (img) {
    const res = await call('nodeBytes', [ncSource().id, img.id, img.imageOffset, img.size - img.imageOffset]);
    if (res.data && ncSelected() === n) $('nc-img').src = objectURL('ncImage', res.data, MIME[img.image] || 'application/octet-stream');
  }
}

async function ncHex(n) {
  const pre = $('nc-hex');
  // 16 bytes per row (76 characters) when the panel is wide enough, else 8 (43 characters);
  // a page is always HEX_ROWS rows.
  const fit = charsThatFit(pre);
  const per = fit >= 76 ? 16 : 8;
  nc.hexPage = per * HEX_ROWS;
  nc.hexPos -= nc.hexPos % nc.hexPage;
  const pos = nc.hexPos;
  const data = (await call('nodeBytes', [ncSource().id, n.id, pos, nc.hexPage])).data || new Uint8Array();
  if (ncSelected() !== n || nc.hexPos !== pos) return; // another row or page was chosen meanwhile
  pre.replaceChildren();
  hexRows(pre, data, { base: n.offset + nc.hexPos, per, ascii: fit >= 43 });
  $('nc-hex-title').textContent = `Hex${n.decompressed ? ' (decompressed)' : ''}`;
  $('nc-hex-pos').textContent = n.size
    ? `${fmtNum(nc.hexPos)}–${fmtNum(nc.hexPos + data.length)} of ${fmtNum(n.size)}`
    : '';
}

function ncHexPage(dir) {
  const n = ncSelected();
  if (!n) return;
  const pos = nc.hexPos + dir * nc.hexPage;
  if (pos < 0 || pos >= n.size) return;
  nc.hexPos = pos;
  ncHex(n);
}

// ncExpand fetches the children of a node that is still lazy on the JavaScript side.
async function ncExpand(n) {
  const src = ncSource();
  $('nc-status').textContent = 'Decompressing…';
  const res = await call('expandNode', [src.id, n.id], 'Decompressing');
  if (res.error) {
    $('nc-status').textContent = `Error: ${res.error}`;
    return false;
  }
  if (n.lazy) {
    n.children = res.children;
    n.lazy = false;
    for (const c of n.children) ncIndex(src, c, n.id);
  }
  return ncSource() === src;
}

async function ncOpen(i) {
  const r = nc.rows[i];
  if (!r) return;
  if (r.up) return ncUp();
  const n = r.node;
  if (n.lazy && !(await ncExpand(n))) return;
  if (!n.children.length || nc.rows[nc.sel]?.node !== n) return;
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

// ncGoLogo selects the logo file. explore returns the path to it, all of it already unpacked.
async function ncGoLogo() {
  await ncEnsure();
  const src = ncSource();
  if (!src) return;
  const logo = src.nodes.get(src.logoPath[src.logoPath.length - 1]);
  if (!logo) {
    $('nc-status').textContent = 'No boot logo file found in this image.';
    return;
  }
  nc.cwd = src.nodes.get(logo.parentId);
  nc.sel = nc.cwd.children.indexOf(logo) + (nc.cwd.parentId !== undefined ? 1 : 0);
  ncRender();
  $('nc-list').focus({ preventScroll: true });
}

function onKey(e) {
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
}

export function initExplorer() {
  events.addEventListener('firmware', (e) => ncSetImage('orig', e.detail));
  events.addEventListener('patched', (e) => ncSetImage('patched', e.detail));
  $('nc-list').addEventListener('keydown', onKey);
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
  new IntersectionObserver((entries) => {
    nc.visible = entries.some((en) => en.isIntersecting);
    if (nc.visible && !$('step-explore').classList.contains('locked')) setTimeout(ncEnsure, 0);
  }).observe($('step-explore'));
}
