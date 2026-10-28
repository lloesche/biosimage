// Hex dumps, used for the logo file headers and the explorer's hex panel.

import { el } from './ui.js';

const hex2 = (b) => b.toString(16).toUpperCase().padStart(2, '0');

// hexRows appends a hex dump of bytes to pre: per bytes (8 or 16) per row in groups of 8, with
// offsets counted from base and an ASCII column unless ascii is false. cls(i), if given,
// returns a CSS class for byte i.
export function hexRows(pre, bytes, { base = 0, per = 16, ascii = true, cls } = {}) {
  for (let row = 0; row < bytes.length; row += per) {
    const parts = [el('span', { className: 'off', textContent: (base + row).toString(16).toUpperCase().padStart(8, '0') }), '  '];
    for (let i = row; i < row + per; i++) {
      const sep = i === row + per - 1 ? '' : (i - row) % 8 === 7 ? '  ' : ' ';
      if (i >= bytes.length) parts.push('  ', sep);
      else parts.push(cls ? el('span', { className: cls(i), textContent: hex2(bytes[i]) }) : hex2(bytes[i]), sep);
    }
    const text = ascii
      ? [...bytes.subarray(row, row + per)].map((b) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '.')).join('')
      : '';
    pre.append(...parts, '  ', el('span', { className: 'asc', textContent: text }), '\n');
  }
}

// fileHeaderView shows the first bytes of an FFS file in container, highlighting the file GUID,
// the rest of the file header and the first section header.
export function fileHeaderView(container, title, bytes, base) {
  const pre = el('pre');
  hexRows(pre, bytes.subarray(0, 64), { base, cls: (i) => (i < 16 ? 'g' : i < 24 ? 'h' : i < 48 ? 's' : '') });
  container.replaceChildren(el('span', { className: 'title', textContent: title }), pre);
}
