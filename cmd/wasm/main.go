//go:build js && wasm

// Command wasm exposes the uefi package to JavaScript for the biosimage web app.
//
// It installs a global `biosimage` object with synchronous functions that take and
// return Uint8Arrays; errors are returned as {error: "..."} instead of exceptions. The web app
// runs it in a Web Worker (web/worker.js), so the long calls do not block the page.
package main

import (
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
		"detail": info.Detail, "warnings": warnings,
	}
}

// firmware is an image opened with open or created by replaceLogo. Its tree is parsed once and
// shared by every call on it, so sections decompressed for one call (e.g. the logo search) serve
// the others.
type firmware struct {
	root     *uefi.Node
	logos    []*uefi.Logo      // nil if no logo was found
	formats  []uefi.LogoFormat // the best known: CurrentFormats, then LogoFormats
	explorer *explorer         // created by the first explore
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

// open(firmware) -> {id, size, volumes, flashNote, capsule?, logos?, formats?, error?}
//
// The image stays open, also when no logo was found (it can still be explored), until close.
func open(args []js.Value) map[string]any {
	fw := bytesFrom(args[0])
	f := &firmware{root: uefi.Parse(fw)}
	res := map[string]any{"id": register(f), "size": len(fw), "volumes": len(uefi.FindVolumes(fw)), "flashNote": f.root.Capsule.FlashNote()}
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

// logoFormats(image) -> {formats: [{format, current, reason}]}, including the formats whose
// decoder is found. It may decompress the whole image, so it is a call of its own.
func logoFormats(args []js.Value) map[string]any {
	f, errRes := imageArg(args[0])
	if f == nil {
		return errRes
	}
	if f.logos == nil {
		return map[string]any{"error": "no logo in this image"}
	}
	f.formats = f.root.LogoFormats(f.logos)
	return map[string]any{"formats": formatsJS(f.formats)}
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

// encodeImage(rgba, width, height, format, quality) -> {data} | {error}
//
// rgba is canvas ImageData; format is "BMP", "JPEG" or "PNG".
func encodeImage(args []js.Value) map[string]any {
	w, h := args[1].Int(), args[2].Int()
	img := &image.RGBA{Pix: bytesFrom(args[0]), Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
	if len(img.Pix) != 4*w*h {
		return map[string]any{"error": "pixel data does not match the size"}
	}
	b, err := uefi.EncodeImage(img, args[3].String(), args[4].Int())
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"data": bytesTo(b)}
}

// replaceLogo(firmware image, image bytes, {noCompress}) -> {id, data, changes, logos} | {error}
//
// id is the patched image, opened from the tree that verified it; logos are read back from it.
func replaceLogo(args []js.Value) map[string]any {
	f, errRes := imageArg(args[0])
	if f == nil {
		return errRes
	}
	var opt uefi.ReplaceOptions
	if len(args) > 2 && args[2].Truthy() {
		opt.NoCompress = args[2].Get("noCompress").Truthy()
	}
	p, err := uefi.ReplaceLogo(f.root.Bytes(), bytesFrom(args[1]), uefi.LogoFileGUID, opt)
	if err != nil {
		return map[string]any{"error": err.Error()}
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
	return map[string]any{"id": id, "data": bytesTo(p.Data), "changes": list, "logos": logosJS(p.Logos)}
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
