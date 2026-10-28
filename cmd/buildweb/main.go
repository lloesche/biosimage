// Command buildweb builds the web app's WebAssembly module into web/ and optionally
// serves the directory for local testing:
//
//	go run ./cmd/buildweb          # writes web/biosimage.wasm and web/wasm_exec.js
//	go run ./cmd/buildweb -serve   # ... and serves web/ on http://localhost:8080
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	dir := flag.String("dir", "web", "web app directory")
	serve := flag.Bool("serve", false, "serve the directory after building")
	addr := flag.String("addr", "localhost:8080", "listen address for -serve")
	version := flag.String("version", "dev", "version shown in the page footer")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(*dir, "biosimage.wasm"), "./cmd/wasm")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		log.Fatalf("building wasm: %v", err)
	}

	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		log.Fatalf("go env GOROOT: %v", err)
	}
	src := filepath.Join(strings.TrimSpace(string(goroot)), "lib", "wasm", "wasm_exec.js")
	js, err := os.ReadFile(src)
	if err != nil {
		log.Fatalf("reading %s: %v", src, err)
	}
	if err := os.WriteFile(filepath.Join(*dir, "wasm_exec.js"), js, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*dir, "version.txt"), []byte(*version+"\n"), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("built %s", *dir)

	if *serve {
		log.Printf("serving %s on http://%s", *dir, *addr)
		log.Fatal(http.ListenAndServe(*addr, http.FileServer(http.Dir(*dir))))
	}
}
