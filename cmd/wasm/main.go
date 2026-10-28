//go:build js && wasm

// Command wasm exposes the uefi package to JavaScript for the biosimage web app.
//
// It installs a global `biosimage` object with synchronous functions that take and
// return Uint8Arrays; errors are returned as {error: "..."} instead of exceptions.
package main

import (
	"fmt"
	"syscall/js"

	"github.com/lloesche/biosimage/uefi"
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("inspectFirmware", export(inspectFirmware))
	api.Set("inspectImage", export(inspectImage))
	api.Set("replaceLogo", export(replaceLogo))
	api.Set("explore", export(explore))
	api.Set("expandNode", export(expandNode))
	api.Set("nodeBytes", export(nodeBytes))
	api.Set("closeExplorer", export(closeExplorer))
	js.Global().Set("biosimage", api)
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

// inspectFirmware(firmware) -> {size, volumes, flashNote, capsule?, logos?, error?}
func inspectFirmware(args []js.Value) map[string]any {
	fw := bytesFrom(args[0])
	root := uefi.Parse(fw)
	res := map[string]any{"size": len(fw), "volumes": len(uefi.FindVolumes(fw)), "flashNote": root.Capsule.FlashNote()}
	if c := root.Capsule; c != nil {
		res["capsule"] = map[string]any{"kind": c.Kind, "headerSize": c.HeaderSize, "signed": c.Signed}
	}
	logos, err := root.FindLogos(uefi.LogoFileGUID)
	if err != nil {
		res["error"] = err.Error()
		return res
	}
	res["logos"] = logosJS(logos)
	return res
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

// inspectImage(image) -> {format, ext, width, height, detail, warnings} | {error}
func inspectImage(args []js.Value) map[string]any {
	info, err := uefi.InspectImage(bytesFrom(args[0]))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return imageInfo(info)
}

// replaceLogo(firmware, image, {noCompress}) -> {data, changes, logos} | {error}
//
// logos are read back from the patched file by a fresh parse, as for inspectFirmware.
func replaceLogo(args []js.Value) map[string]any {
	fw, img := bytesFrom(args[0]), bytesFrom(args[1])
	var opt uefi.ReplaceOptions
	if len(args) > 2 && args[2].Truthy() {
		opt.NoCompress = args[2].Get("noCompress").Truthy()
	}
	p, err := uefi.ReplaceLogo(fw, img, uefi.LogoFileGUID, opt)
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
	return map[string]any{"data": bytesTo(p.Data), "changes": list, "logos": logosJS(p.Logos)}
}

// Explorers keep a parsed image (and the sections decompressed so far) between calls.
var (
	explorers    = map[int]*explorer{}
	nextExplorer = 1
)

func explorerArg(args []js.Value) (*explorer, map[string]any) {
	e := explorers[args[0].Int()]
	if e == nil {
		return nil, map[string]any{"error": "unknown explorer"}
	}
	return e, nil
}

// explore(firmware) -> {id, root, logoPath}
func explore(args []js.Value) map[string]any {
	e := newExplorer(bytesFrom(args[0]))
	id := nextExplorer
	nextExplorer++
	explorers[id] = e
	root := e.nodeJS(view{n: e.root}, true)
	path := make([]any, 0)
	for _, id := range e.logoPath() {
		path = append(path, id)
	}
	return map[string]any{"id": id, "root": root, "logoPath": path}
}

// expandNode(explorer, node) -> node with children (decompressing lazy sections)
func expandNode(args []js.Value) map[string]any {
	e, errRes := explorerArg(args)
	if e == nil {
		return errRes
	}
	n, err := e.expand(args[1].Int())
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return n
}

// nodeBytes(explorer, node, offset, length) -> {data}
func nodeBytes(args []js.Value) map[string]any {
	e, errRes := explorerArg(args)
	if e == nil {
		return errRes
	}
	return map[string]any{"data": bytesTo(e.bytes(args[1].Int(), args[2].Int(), args[3].Int()))}
}

// closeExplorer(explorer) releases the image.
func closeExplorer(args []js.Value) map[string]any {
	delete(explorers, args[0].Int())
	return map[string]any{}
}
