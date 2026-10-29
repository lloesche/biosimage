// Shared state of the web app, and events for the parts that follow it.

export const state = {
  fwId: 0, // the loaded BIOS, opened in the worker (biosimage.open)
  fwName: '',
  fwInfo: null, // result of biosimage.open
  formats: null, // output formats offered for the BIOS: [{format, current, reason}]
  source: null, // the new logo: { img, bytes, name, info, decodable }
  built: false,
  screen: null, // the step the cursor is on
};

// Events:
//   firmware: a BIOS file was loaded; detail is its image ID (0 if it cannot be explored)
//   patched:  a patched file was created; detail is its image ID (0 once it no longer matches
//             the inputs)
//   logo:     the logo to patch in changed
//   warnings: the image warnings, which can block patching, changed
export const events = new EventTarget();

export const emit = (type, detail) => events.dispatchEvent(new CustomEvent(type, { detail }));
