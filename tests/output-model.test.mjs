import assert from 'node:assert/strict';
import { test } from 'node:test';
import { outputChoices, selectOutput } from '../web/output-model.js';

const basic = [{ format: 'BMP' }, { format: 'JPEG' }];
const native = [...basic, { format: 'GIF' }];
const storage = { compress: true, freeSpace: true };

test('DXE requires both decoded animation and proven firmware support', () => {
  for (const animated of [false, true]) for (const supported of [false, true]) {
    const choices = outputChoices(basic, animated, supported);
    assert.equal(choices.some((c) => c.kind === 'dxe'), animated && supported);
    assert.equal(choices[0].kind, 'static'); // never the default
    assert.equal(choices.some((c) => c.value === 'GIF'), false);
  }
});

test('native GIF and DXE are separate choices with distinct timing and storage', () => {
  const choices = outputChoices(native, true, true);
  const gif = selectOutput(choices, 'GIF', 'GIF', storage);
  const dxe = selectOutput(choices, 'dxe', 'GIF', storage);
  assert.equal(gif.kind, 'native-gif');
  assert.equal(gif.encodedKey, 'data');
  assert.deepEqual(gif.options, storage);
  assert.equal(gif.frameStorage, true);
  assert.equal(gif.requiresFit, false);
  assert.equal(dxe.kind, 'dxe');
  assert.equal(dxe.encodedKey, 'preview');
  assert.deepEqual(dxe.options, { animateDXE: true, once: false, logoFrame: 0 });
  assert.equal(dxe.frameStorage, false);
  assert.equal(dxe.logoStorage, false);
  assert.equal(dxe.requiresFit, true);
  assert.equal(gif.animated && dxe.animated, true);
});

test('static GIF and file-as-is do not opt into animation processing', () => {
  const choices = outputChoices(native, false, true);
  for (const mode of ['GIF', 'asis']) {
    const selected = selectOutput(choices, mode, 'GIF', storage);
    assert.equal(selected.kind, 'static');
    assert.equal(selected.animated, false);
    assert.equal(selected.encodedKey, 'data');
    assert.equal(selected.asIs, mode === 'asis');
    assert.deepEqual(selected.options, storage);
  }
});

test('a new firmware or static source cannot retain an unavailable DXE selection', () => {
  for (const choices of [outputChoices(native, true, false), outputChoices(native, false, true)]) {
    assert.throws(() => selectOutput(choices, 'dxe', 'GIF', storage), /unavailable/);
  }
});

test('captured options stay paired with the output while controls change', () => {
  const options = { compress: true, freeSpace: false };
  const choices = outputChoices(native, true, true);
  const before = selectOutput(choices, 'GIF', 'GIF', options);
  options.compress = false;
  const after = selectOutput(choices, 'dxe', 'GIF', options);
  assert.deepEqual(before.options, { compress: true, freeSpace: false });
  assert.equal(before.encodedKey, 'data');
  assert.deepEqual(after.options, { animateDXE: true, once: false, logoFrame: 0 });
  assert.equal(after.encodedKey, 'preview');
});

test('play once only applies to the driver animation', () => {
  const choices = outputChoices(native, true, true);
  const dxe = selectOutput(choices, 'dxe', 'GIF', storage, { once: true, logoFrame: 3 });
  assert.equal(dxe.plays, 1);
  assert.deepEqual(dxe.options, { animateDXE: true, once: true, logoFrame: 3 });
  const gif = selectOutput(choices, 'GIF', 'GIF', storage, { once: true, logoFrame: 3 }); // AMI's player always loops
  assert.equal(gif.plays, 0);
  assert.deepEqual(gif.options, storage);
});
