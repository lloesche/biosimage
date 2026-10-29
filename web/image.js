// Reading the new logo with the browser's image decoders.

import { objectURL } from './ui.js';

// sizeSVG gives SVGs without width/height an intrinsic size from their viewBox, so every
// browser can draw them onto a canvas at high resolution.
export function sizeSVG(text) {
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
export function decodeImage(blob) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => (img.naturalWidth && img.naturalHeight ? resolve(img) : reject(new Error('image has no size')));
    img.onerror = () => reject(new Error('cannot decode image'));
    img.src = objectURL('source', blob);
  });
}
