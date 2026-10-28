// Shared state of the web app, and events for the parts that follow it (the explorer).

export const state = {
  fw: null, // Uint8Array
  fwName: '',
  fwInfo: null, // result of biosimage.inspectFirmware
  source: null, // { img, bytes, name, info }
  built: false,
  screen: null, // the step the cursor is on
};

// Events, each with the bytes in event.detail:
//   firmware: a BIOS file was loaded (null if it cannot be explored)
//   patched:  a patched file was created (null once it no longer matches the inputs)
export const events = new EventTarget();

export const emit = (type, detail) => events.dispatchEvent(new CustomEvent(type, { detail }));
