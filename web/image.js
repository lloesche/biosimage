// Reading the new logo and encoding it as BMP, with the browser's image decoders and canvas.

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

// canvasToBMP encodes the canvas as an uncompressed, bottom-up 24-bit BMP.
export function canvasToBMP(canvas) {
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
