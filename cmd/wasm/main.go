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

// inspectFirmware(firmware) -> {size, volumes, capsule?, logos?, error?}
func inspectFirmware(args []js.Value) map[string]any {
	fw := bytesFrom(args[0])
	res := map[string]any{"size": len(fw), "volumes": len(uefi.FindVolumes(fw))}
	if c := uefi.DetectCapsule(fw); c != nil {
		res["capsule"] = map[string]any{"kind": c.Kind, "headerSize": c.HeaderSize, "signed": c.Signed}
	}
	logos, err := uefi.FindLogos(fw, uefi.LogoFileGUID)
	if err != nil {
		res["error"] = err.Error()
		return res
	}
	list := make([]any, 0, len(logos))
	for _, l := range logos {
		m := map[string]any{
			"guid": l.File.GUID.String(), "fileOffset": l.File.Offset, "fileSize": l.File.Size,
			"volumeOffset": l.Volume.Offset, "volumeLength": l.Volume.Length,
			"encoding": l.Encoding.String(), "detail": l.Detail, "name": l.Name, "capacity": l.Capacity,
		}
		if l.Image != nil {
			m["image"] = bytesTo(l.Image)
			if info, err := uefi.InspectImage(l.Image); err == nil {
				m["imageInfo"] = imageInfo(info)
			}
		}
		list = append(list, m)
	}
	res["logos"] = list
	return res
}

// inspectImage(image) -> {format, ext, width, height, detail, warnings} | {error}
func inspectImage(args []js.Value) map[string]any {
	info, err := uefi.InspectImage(bytesFrom(args[0]))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return imageInfo(info)
}

// replaceLogo(firmware, image, {noCompress}) -> {data, changes} | {error}
func replaceLogo(args []js.Value) map[string]any {
	fw, img := bytesFrom(args[0]), bytesFrom(args[1])
	var opt uefi.ReplaceOptions
	if len(args) > 2 && args[2].Truthy() {
		opt.NoCompress = args[2].Get("noCompress").Truthy()
	}
	out, changes, err := uefi.ReplaceLogo(fw, img, uefi.LogoFileGUID, opt)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	list := make([]any, len(changes))
	for i, c := range changes {
		list[i] = map[string]any{
			"volumeOffset": c.VolumeOffset, "oldOffset": c.OldOffset, "newOffset": c.NewOffset,
			"oldSize": c.OldSize, "newSize": c.NewSize, "placement": c.Placement, "storage": c.Storage,
		}
	}
	return map[string]any{"data": bytesTo(out), "changes": list}
}
