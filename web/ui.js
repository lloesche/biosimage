// DOM and formatting helpers shared by the web app's modules.

export const $ = (id) => document.getElementById(id);

export const MIME = { BMP: 'image/bmp', PNG: 'image/png', JPEG: 'image/jpeg', GIF: 'image/gif' };

export const fmtBytes = (n) => {
  const [v, u] = n < 1024 ? [n, 'B'] : n < 1 << 20 ? [n / 1024, 'KiB'] : [n / (1 << 20), 'MiB'];
  return `${Number.isInteger(v) ? v : v.toFixed(1)} ${u}`;
};
export const fmtNum = (n) => n.toLocaleString('en-US');
export const hex8 = (n) => '0x' + n.toString(16).toUpperCase().padStart(8, '0');
export const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, Math.round(Number(v) || 0)));
export const el = (tag, props = {}, ...children) => {
  const e = Object.assign(document.createElement(tag), props);
  e.append(...children);
  return e;
};

export function setError(node, msg) {
  node.textContent = msg || '';
  node.hidden = !msg;
}

export function setLocked(id, locked) {
  const node = $(id);
  node.classList.toggle('locked', locked);
  node.inert = locked;
}

const urls = {};

// objectURL replaces the blob URL stored under key, revoking the previous one.
export function objectURL(key, data, type) {
  if (urls[key]) URL.revokeObjectURL(urls[key]);
  urls[key] = URL.createObjectURL(data instanceof Blob ? data : new Blob([data], { type }));
  return urls[key];
}

// charsThatFit returns how many monospace characters fit into an element's content width.
export function charsThatFit(node) {
  const probe = el('span', { textContent: 'M'.repeat(20) });
  probe.style.cssText = 'position:absolute;visibility:hidden;white-space:pre';
  node.append(probe);
  const w = probe.getBoundingClientRect().width / 20;
  probe.remove();
  const style = getComputedStyle(node);
  const inner = node.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
  return w > 0 ? Math.floor(inner / w) : 80;
}

// showHelp renders text with blank-line separated paragraphs; lines starting with "1." form a list.
export function showHelp(text) {
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

// initHelp shows an element's data-help text in the help column while it is hovered or focused.
export function initHelp() {
  const onHelpTarget = (e) => {
    const t = e.target.closest?.('[data-help]');
    if (t) showHelp(t.dataset.help);
  };
  document.addEventListener('mouseover', onHelpTarget);
  document.addEventListener('focusin', onHelpTarget);
}
