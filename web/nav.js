// Tabs and keyboard navigation. Like a BIOS setup: ↑/↓ select an item, ←/→ switch screens
// (sliders and radio buttons keep ←/→ for their value), +/- change a value, Enter selects,
// Home/End jump to the ends.

import { $ } from './ui.js';
import { state } from './state.js';

const SCREENS = ['fw', 'img', 'build', 'explore'];
const ITEM_SELECTOR = 'label.drop, input:not([type=hidden]):not([type=file]), button, a.selbar, .nc-list';

// The highlighted tab is the screen the cursor is on, or else the next step to do.
export function updateTabs() {
  const done = { fw: !!state.fwInfo, img: !!state.source && !!state.fwInfo, build: state.built };
  const active = state.screen || (!done.fw ? 'fw' : !done.img ? 'img' : 'build');
  for (const k of SCREENS) {
    $(`tab-${k}`).classList.toggle('done', !!done[k]);
    $(`tab-${k}`).classList.toggle('on', k === active);
  }
}

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

function onKey(e) {
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
}

export function initNav() {
  document.addEventListener('keydown', onKey);
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
}
