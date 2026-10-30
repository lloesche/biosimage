// Output choices are the source of truth for playback, encoding and storage.
// DXE is a separate opt-in choice, never an implicit variant of native GIF.
export function outputChoices(formats, hasAnimation, dxeSupported) {
  const choices = formats.map(({ format }) => ({
    value: format, format, label: format,
    kind: format === 'GIF' && hasAnimation ? 'native-gif' : 'static',
  }));
  if (hasAnimation && dxeSupported) choices.push({
    value: 'dxe', format: 'GIF', kind: 'dxe', label: 'Animate during POST (experimental)',
  });
  return choices;
}

// Resolve one snapshot before starting asynchronous encoding. Bytes and storage
// options must always describe the same selection, even if controls change later.
export function selectOutput(choices, value, sourceFormat, logoOptions) {
  const asIs = value === 'asis';
  const choice = asIs ? { value, format: sourceFormat, kind: 'static' }
    : choices.find((c) => c.value === value);
  if (!choice) throw new Error('The selected output is unavailable for this image and firmware.');
  const dxe = choice.kind === 'dxe';
  return {
    ...choice, asIs,
    animated: choice.kind !== 'static',
    frameStorage: choice.kind === 'native-gif',
    logoStorage: !dxe,
    requiresFit: dxe,
    encodedKey: dxe ? 'preview' : 'data', // DXE uses each frame's own delay.
    options: dxe ? { animateDXE: true } : { ...logoOptions },
  };
}
