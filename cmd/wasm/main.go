//go:build js && wasm

// Command wasm exposes the uefi package to JavaScript for the biosimage web app.
//
// It installs a global `biosimage` object with synchronous functions that take and
// return Uint8Arrays; errors are returned as {error: "..."} instead of exceptions. The web app
// runs it in a Web Worker (web/worker.js), so the long calls do not block the page.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"syscall/js"

	"github.com/lloesche/biosimage/uefi"
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("open", export(open))
	api.Set("inspectImage", export(inspectImage))
	api.Set("logoFormats", export(logoFormats))
	api.Set("encodeImage", export(encodeImage))
	api.Set("decodeAnimation", export(decodeAnimation))
	api.Set("encodeAnimation", export(encodeAnimation))
	api.Set("fitLogo", export(fitLogo))
	api.Set("replaceLogo", export(replaceLogo))
	api.Set("explore", export(explore))
	api.Set("expandNode", export(expandNode))
	api.Set("nodeBytes", export(nodeBytes))
	api.Set("close", export(closeImage))
	js.Global().Set("biosimage", api)
	// Long LZMA operations report progress to biosimageProgress(op, done, total), if defined.
	uefi.Progress = func(op string, done, total int) {
		if f := js.Global().Get("biosimageProgress"); f.Type() == js.TypeFunction {
			f.Invoke(op, done, total)
		}
	}
	select {}
}

// export wraps fn so that a Go panic is reported to JavaScript instead of killing the runtime.
func export(fn func(args []js.Value) map[string]any) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) (result any) {
		defer func() {
			if r := recover(); r != nil {
				result = map[string]any{"error": fmt.Sprint("internal error: ", r)}
			}
		}()
		return fn(args)
	})
}

func bytesFrom(v js.Value) []byte {
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}

func bytesTo(b []byte) js.Value {
	a := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(a, b)
	return a
}

func imageInfo(info *uefi.ImageInfo) map[string]any {
	warnings := make([]any, len(info.Warnings))
	for i, w := range info.Warnings {
		warnings[i] = w
	}
	return map[string]any{
		"format": info.Format, "ext": info.Ext, "width": info.Width, "height": info.Height,
		"detail": info.Detail, "frames": info.Frames, "warnings": warnings,
	}
}

// firmware is an image opened with open or created by replaceLogo. Its tree is parsed once and
// shared by every call on it, so sections decompressed for one call (e.g. the logo search) serve
// the others.
type firmware struct {
	root      *uefi.Node
	logos     []*uefi.Logo      // nil if no logo was found
	formats   []uefi.LogoFormat // the best known: CurrentFormats, then LogoFormats
	free      *uefi.FreeSpace   // found by logoFormats; nil if there is none
	bootGuard uefi.BootGuard    // what the image shows of Intel Boot Guard
	explorer  *explorer         // created by the first explore
	// animation is the last animation patch built by fitLogo, for replaceLogo to reuse: building
	// one compresses every frame and takes seconds.
	animation *animationPatch
}

type animationPatch struct {
	gif   []byte
	patch *uefi.Patch
}

// animatedLogo returns the animation patch of the image for gif (Node.ReplaceAnimatedLogo),
// reusing the one fitLogo built.
func (f *firmware) animatedLogo(gif []byte) (*uefi.Patch, error) {
	if a := f.animation; a != nil && bytes.Equal(a.gif, gif) {
		return a.patch, nil
	}
	p, err := f.root.ReplaceAnimatedLogo(gif, uefi.LogoFileGUID)
	f.animation = nil
	if err == nil {
		f.animation = &animationPatch{gif: bytes.Clone(gif), patch: p}
	}
	return p, err
}

var (
	images    = map[int]*firmware{}
	nextImage = 1
)

func register(f *firmware) int {
	id := nextImage
	nextImage++
	images[id] = f
	return id
}

func imageArg(v js.Value) (*firmware, map[string]any) {
	f := images[v.Int()]
	if f == nil {
		return nil, map[string]any{"error": "unknown image"}
	}
	return f, nil
}

// open(firmware) -> {id, size, volumes, flashNote, bootGuard, capsule?, logos?, formats?, error?}
//
// The image stays open, also when no logo was found (it can still be explored), until close.
// bootGuard describes what the image shows of Intel Boot Guard ("" if nothing).
func open(args []js.Value) map[string]any {
	fw := bytesFrom(args[0])
	f := &firmware{root: uefi.Parse(fw), bootGuard: uefi.DetectBootGuard(fw)}
	res := map[string]any{"id": register(f), "size": len(fw), "volumes": len(uefi.FindVolumes(fw)), "flashNote": f.root.Capsule.FlashNote(),
		"bootGuard": f.bootGuard.Note()}
	if c := f.root.Capsule; c != nil {
		res["capsule"] = map[string]any{"kind": c.Kind, "headerSize": c.HeaderSize, "signed": c.Signed}
	}
	logos, err := f.root.FindLogos(uefi.LogoFileGUID)
	if err != nil {
		res["error"] = err.Error()
		return res
	}
	f.logos, f.formats = logos, uefi.CurrentFormats(logos)
	res["logos"], res["formats"] = logosJS(logos), formatsJS(f.formats)
	return res
}

// close(image) releases an image.
func closeImage(args []js.Value) map[string]any {
	delete(images, args[0].Int())
	return map[string]any{}
}

// logoFormats(image) -> {formats: [{format, current, reason}], freeSpace}, including the
// formats whose decoder is found, and the capacity of the free space the logo can be moved to
// (0 if none, uefi.Node.FreeSpace). It may decompress the whole image, so it is a call of its
// own.
func logoFormats(args []js.Value) map[string]any {
	f, errRes := imageArg(args[0])
	if f == nil {
		return errRes
	}
	if f.logos == nil {
		return map[string]any{"error": "no logo in this image"}
	}
	f.formats = f.root.LogoFormats(f.logos)
	res := map[string]any{"formats": formatsJS(f.formats), "freeSpace": 0}
	res["animationDXE"] = false
	if err := f.root.AnimationSupport(); err == nil {
		res["animationDXE"] = true
	} else {
		res["animationReason"] = err.Error()
	}
	if free, err := f.root.FreeSpace(); err == nil {
		f.free, res["freeSpace"] = free, free.Capacity
	}
	return res
}

func formatsJS(formats []uefi.LogoFormat) []any {
	list := make([]any, len(formats))
	for i, f := range formats {
		list[i] = map[string]any{"format": f.Format, "current": f.Current, "reason": f.Reason()}
	}
	return list
}

func logosJS(logos []*uefi.Logo) []any {
	list := make([]any, len(logos))
	for i, l := range logos {
		enclosing := make([]any, 0)
		for _, h := range l.Enclosing() {
			enclosing = append(enclosing, map[string]any{
				"guid": h.File.GUID.String(), "fileOffset": h.File.Offset, "fileSize": h.File.Size, "compressed": h.Compressed,
			})
		}
		m := map[string]any{
			"guid": l.File.GUID.String(), "fileOffset": l.File.Offset, "fileSize": l.File.Size,
			"volumeOffset": l.Volume.Offset, "volumeLength": l.Volume.Length,
			"storage": l.Storage(), "name": l.Name, "capacity": l.Capacity,
			"compressed": l.Compressed(), "enclosing": enclosing, "header": bytesTo(l.Header(64)),
			"lzma": l.Encoding == uefi.EncodingLZMA, "rawFile": l.Encoding == uefi.EncodingRawFile,
		}
		if l.Image != nil {
			m["image"] = bytesTo(l.Image)
			if info, err := uefi.InspectImage(l.Image); err == nil {
				m["imageInfo"] = imageInfo(info)
			}
		}
		list[i] = m
	}
	return list
}

// inspectImage(image bytes, firmware image?) -> {format, ext, width, height, detail, warnings} | {error}
//
// With a firmware image, the warnings include a format that it most likely cannot show.
func inspectImage(args []js.Value) map[string]any {
	info, err := uefi.InspectImage(bytesFrom(args[0]))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if len(args) > 1 && !args[1].IsUndefined() && !args[1].IsNull() {
		if f, errRes := imageArg(args[1]); f == nil {
			return errRes
		} else if f.formats != nil {
			info.CheckSupported(f.formats)
		}
	}
	return imageInfo(info)
}

// rgbaArg returns canvas ImageData of the given size as an image.
func rgbaArg(v js.Value, w, h int) (*image.RGBA, error) {
	img := &image.RGBA{Pix: bytesFrom(v), Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
	if len(img.Pix) != 4*w*h {
		return nil, errors.New("pixel data does not match the size")
	}
	return img, nil
}

// encodeImage(rgba, width, height, format, quality) -> {data} | {error}
//
// rgba is canvas ImageData; format is "BMP", "JPEG", "PNG" or "GIF".
func encodeImage(args []js.Value) map[string]any {
	img, err := rgbaArg(args[0], args[1].Int(), args[2].Int())
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	b, err := uefi.EncodeImage(img, args[3].String(), args[4].Int())
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"data": bytesTo(b)}
}

// decodeAnimation(gif bytes) -> {width, height, frames: [rgba], delays, timing} | {error}
//
// The frames are complete (uefi.DecodeAnimation); delays are in units of 10 ms. timing.speed and
// timing.all are the frames to store, [{index, delay}], to keep the speed or all frames
// (uefi.Animation.Timing).
func decodeAnimation(args []js.Value) map[string]any {
	a, err := uefi.DecodeAnimation(bytesFrom(args[0]))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	frames, delays := make([]any, len(a.Frames)), make([]any, len(a.Delays))
	for i, f := range a.Frames {
		frames[i], delays[i] = bytesTo(f.Pix), a.Delays[i]
	}
	timing := func(keepSpeed bool) []any {
		var list []any
		for _, f := range a.Timing(keepSpeed) {
			list = append(list, map[string]any{"index": f.Index, "delay": f.Delay})
		}
		return list
	}
	r := a.Frames[0].Rect
	return map[string]any{"width": r.Dx(), "height": r.Dy(), "frames": frames, "delays": delays,
		"timing": map[string]any{"speed": timing(true), "all": timing(false)}}
}

// encodeAnimation([rgba], width, height, delays, full) -> {data, preview} | {error}: an
// animated GIF, with complete frames if full is set, else only the changed areas
// (uefi.EncodeGIF). data is timed for the firmware (uefi.FirmwareTiming), preview for browsers.
func encodeAnimation(args []js.Value) map[string]any {
	w, h := args[1].Int(), args[2].Int()
	frames, delays := make([]image.Image, args[0].Length()), make([]int, args[3].Length())
	for i := range frames {
		img, err := rgbaArg(args[0].Index(i), w, h)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		frames[i] = img
	}
	for i := range delays {
		delays[i] = args[3].Index(i).Int()
	}
	preview, err := uefi.EncodeGIF(frames, delays, args[4].Truthy())
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	data, err := uefi.FirmwareTiming(preview)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"data": bytesTo(data), "preview": bytesTo(preview)}
}

// fitLogo(firmware image, image bytes, {freeSpace})
// -> {size, capacity, storage, fits, bootGuard, error?}
//
// size is the logo file that replaceLogo would write (uefi.Logo.StoredSize), for the logo file
// with the least room if there are several; error says why it does not fit. bootGuard warns
// that Intel Boot Guard may keep the board from starting the patched image ("" if not).
func fitLogo(args []js.Value) map[string]any {
	f, errRes := imageArg(args[0])
	if f == nil {
		return errRes
	}
	if f.logos == nil {
		return map[string]any{"error": "no logo in this image"}
	}
	if animationOption(args, 2) {
		p, err := f.animatedLogo(bytesFrom(args[1]))
		res := map[string]any{"animationDXE": true, "fits": err == nil, "bootGuard": "", "storage": animationStorage}
		if err != nil {
			res["error"] = err.Error()
		} else {
			res["size"], res["capacity"] = p.AnimationSize, p.AnimationRoom
		}
		return res
	}
	opt, err := optionsArg(f, args, 2)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var free *uefi.FreeSpace
	if opt.UseFreeSpace {
		free = f.free
	}
	res := fitResult(f, bytesFrom(args[1]), opt)
	res["bootGuard"] = f.bootGuard.Warning(f.logos, free)
	return res
}

func fitResult(f *firmware, img []byte, opt uefi.ReplaceOptions) map[string]any {
	if opt.UseFreeSpace {
		size, storage, err := f.free.StoredSize(f.logos[0], img, opt)
		res := map[string]any{"size": size, "capacity": f.free.Capacity, "storage": storage.String(), "fits": err == nil}
		if err != nil {
			res["error"] = err.Error()
		}
		return res
	}
	type fit struct {
		size, room int
		storage    uefi.Storage
		err        error
	}
	var worst *fit
	for _, l := range f.logos {
		r := &fit{room: l.Capacity}
		r.size, r.storage, r.err = l.StoredSize(img, opt)
		failing, worstFailing := r.err != nil, worst != nil && worst.err != nil
		if worst == nil || failing && !worstFailing || failing == worstFailing && r.room-r.size < worst.room-worst.size {
			worst = r
		}
	}
	res := map[string]any{"size": worst.size, "capacity": worst.room, "storage": worst.storage.String(), "fits": worst.err == nil}
	if worst.err != nil {
		res["error"] = worst.err.Error()
	}
	return res
}

// replaceLogo(firmware image, image bytes, {compress, freeSpace, bootGuardOK})
// -> {id, data, changes, logos, capacity, bootGuard} | {error}
//
// id is the patched image, opened from the tree that verified it; logos are read back from it.
// bootGuard warns that Intel Boot Guard may keep the board from starting it ("" if not); such
// an image is only returned with bootGuardOK, like the CLI's -force.
func replaceLogo(args []js.Value) map[string]any {
	f, errRes := imageArg(args[0])
	if f == nil {
		return errRes
	}
	opt, err := optionsArg(f, args, 2)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var p *uefi.Patch
	if animationOption(args, 2) {
		p, err = f.animatedLogo(bytesFrom(args[1]))
		f.animation = nil // the patched image is opened on its own
	} else {
		p, err = uefi.ReplaceLogo(f.root.Bytes(), bytesFrom(args[1]), uefi.LogoFileGUID, opt)
	}
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if p.BootGuardWarning != "" && !(len(args) > 2 && args[2].Truthy() && args[2].Get("bootGuardOK").Truthy()) {
		return map[string]any{"error": p.BootGuardWarning + " Confirm that you can restore the original BIOS without booting to patch it anyway."}
	}
	list := make([]any, len(p.Changes))
	for i, c := range p.Changes {
		recompressed := make([]any, len(c.Recompressed))
		for j, r := range c.Recompressed {
			recompressed[j] = r.String()
		}
		list[i] = map[string]any{
			"volumeOffset": c.VolumeOffset, "oldOffset": c.OldOffset, "newOffset": c.NewOffset,
			"oldSize": c.OldSize, "newSize": c.NewSize, "placement": c.Placement.String(), "storage": c.Storage.String(),
			"recompressed": recompressed,
		}
	}
	id := register(&firmware{root: p.Root, logos: p.Logos, formats: f.formats})
	capacity := f.logos[0].Capacity
	if opt.UseFreeSpace {
		capacity = f.free.Capacity
	}
	res := map[string]any{"id": id, "data": bytesTo(p.Data), "changes": list, "logos": logosJS(p.Logos),
		"capacity": capacity, "bootGuard": p.BootGuardWarning, "animationDXE": animationOption(args, 2)}
	if animationOption(args, 2) {
		res["animationSize"], res["animationRoom"], res["animationStorage"] = p.AnimationSize, p.AnimationRoom, animationStorage
	}
	return res
}

// animationStorage describes where ReplaceAnimatedLogo puts the animation.
const animationStorage = "driver and frames LZMA-compressed in the free space of the main volume; first frame as the logo"

func animationOption(args []js.Value, i int) bool {
	return len(args) > i && args[i].Truthy() && args[i].Get("animateDXE").Truthy()
}

// optionsArg returns the ReplaceOptions in args[i], if given: {compress, freeSpace}. compress
// true stores the image LZMA-compressed, false uncompressed, and without it like the original.
func optionsArg(f *firmware, args []js.Value, i int) (uefi.ReplaceOptions, error) {
	var opt uefi.ReplaceOptions
	if len(args) > i && args[i].Truthy() {
		if c := args[i].Get("compress"); c.Type() == js.TypeBoolean {
			opt.Compression = map[bool]uefi.Compression{true: uefi.CompressLZMA, false: uefi.CompressNone}[c.Bool()]
		}
		opt.UseFreeSpace = args[i].Get("freeSpace").Truthy()
	}
	if opt.UseFreeSpace && f.free == nil {
		return opt, uefi.ErrNoFreeSpace
	}
	return opt, nil
}

// explore(image) -> {root, logoPath}: the structure tree, as far as it is unpacked, and the node
// IDs from the root to the logo file.
func explore(args []js.Value) map[string]any {
	f, errRes := imageArg(args[0])
	if f == nil {
		return errRes
	}
	if f.explorer == nil {
		f.explorer = newExplorer(f.root, f.logos)
	}
	e := f.explorer
	root := e.nodeJS(view{n: e.root}, true)
	path := make([]any, 0)
	for _, id := range e.logoPath() {
		path = append(path, id)
	}
	return map[string]any{"root": root, "logoPath": path}
}

// explorerArg returns the explorer of an image that explore was called on.
func explorerArg(v js.Value) (*explorer, map[string]any) {
	f, errRes := imageArg(v)
	if f == nil {
		return nil, errRes
	}
	if f.explorer == nil {
		return nil, map[string]any{"error": "image not explored"}
	}
	return f.explorer, nil
}

// expandNode(image, node) -> node with children (decompressing lazy sections)
func expandNode(args []js.Value) map[string]any {
	e, errRes := explorerArg(args[0])
	if e == nil {
		return errRes
	}
	n, err := e.expand(args[1].Int())
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return n
}

// nodeBytes(image, node, offset, length) -> {data}
func nodeBytes(args []js.Value) map[string]any {
	e, errRes := explorerArg(args[0])
	if e == nil {
		return errRes
	}
	return map[string]any{"data": bytesTo(e.bytes(args[1].Int(), args[2].Int(), args[3].Int()))}
}
