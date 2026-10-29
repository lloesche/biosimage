package uefi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// vendorFirmware is one entry of testdata/firmware.json: an official BIOS download and
// what biosimage is expected to find in it.
type vendorFirmware struct {
	Vendor   string `json:"vendor"`
	Board    string `json:"board"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	File     string `json:"file"`
	Expect   struct {
		Signed      bool   `json:"signed"`
		Logos       int    `json:"logos"`
		FileOffset  int    `json:"fileOffset"`
		Encoding    string `json:"encoding"`
		Image       string `json:"image"`
		Capacity    int    `json:"capacity"`
		Compressed  bool   `json:"inCompressedVolume"`
		Unsupported string `json:"unsupported"`
		Formats     string `json:"formats"`
	} `json:"expect"`
}

const userAgent = "biosimage-ci (+https://github.com/lloesche/biosimage)"

// TestVendorFirmware runs against real vendor BIOS images listed in testdata/firmware.json.
// The files are downloaded from the vendors' servers into BIOSIMAGE_FIRMWARE_CACHE and
// never committed or published; the test is skipped when the variable is unset.
func TestVendorFirmware(t *testing.T) {
	cache := os.Getenv("BIOSIMAGE_FIRMWARE_CACHE")
	if cache == "" {
		t.Skip("set BIOSIMAGE_FIRMWARE_CACHE to a directory to download and test vendor firmware")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "testdata", "firmware.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []vendorFirmware
	if err := json.Unmarshal(manifest, &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Run(fmt.Sprintf("%s %s %s", e.Vendor, e.Board, e.Version), func(t *testing.T) {
			fw, err := fetchFirmware(cache, e)
			if err != nil {
				t.Fatal(err)
			}
			checkVendorFirmware(t, e, fw)
		})
	}
}

func checkVendorFirmware(t *testing.T, e vendorFirmware, fw []byte) {
	root := Parse(fw)
	logos, err := root.FindLogos(LogoFileGUID)
	if e.Expect.Unsupported != "" {
		if err == nil {
			t.Fatalf("expected %q, but %d logo(s) were found: support was added, update testdata/firmware.json", e.Expect.Unsupported, len(logos))
		}
		if !strings.Contains(err.Error(), e.Expect.Unsupported) {
			t.Fatalf("expected error containing %q, got %v", e.Expect.Unsupported, err)
		}
		t.Logf("unsupported as expected: %v", err)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	c := DetectCapsule(fw)
	if signed := c != nil && c.Signed; signed != e.Expect.Signed {
		t.Errorf("signed = %v, want %v", signed, e.Expect.Signed)
	}
	if len(logos) != e.Expect.Logos {
		t.Fatalf("found %d logos, want %d", len(logos), e.Expect.Logos)
	}
	l := logos[0]
	if l.File.Offset != e.Expect.FileOffset {
		t.Errorf("logo at %#x, want %#x", l.File.Offset, e.Expect.FileOffset)
	}
	if got := l.Encoding.String(); got != e.Expect.Encoding {
		t.Errorf("encoding %q, want %q", got, e.Expect.Encoding)
	}
	if l.Compressed() != e.Expect.Compressed {
		t.Errorf("inside compressed volume = %v, want %v", l.Compressed(), e.Expect.Compressed)
	}
	if l.Capacity != e.Expect.Capacity {
		t.Errorf("capacity %d, want %d", l.Capacity, e.Expect.Capacity)
	}
	var formats []string
	for _, f := range root.LogoFormats(logos) {
		formats = append(formats, f.Format)
	}
	if got := strings.Join(formats, " "); got != e.Expect.Formats {
		t.Errorf("formats %q, want %q", got, e.Expect.Formats)
	}
	info, err := InspectImage(l.Image)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%s %dx%d", info.Format, info.Width, info.Height); got != e.Expect.Image {
		t.Errorf("image %q, want %q", got, e.Expect.Image)
	}

	img := makeBMP(320, 180, false)
	p, err := ReplaceLogo(fw, img, LogoFileGUID, ReplaceOptions{})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	again, err := FindLogos(p.Data, LogoFileGUID)
	if err != nil || len(again) != len(logos) {
		t.Fatalf("re-read after replace: %d logos, %v", len(again), err)
	}
	for _, l := range again {
		if !bytes.Equal(l.Image, img) {
			t.Fatalf("logo at %#x does not decode to the new image", l.File.Offset)
		}
	}
	for i, c := range p.Changes {
		if size, storage, err := logos[i].StoredSize(img, ReplaceOptions{}); err != nil || size != c.NewSize || storage != c.Storage {
			t.Errorf("StoredSize %d %v %v, ReplaceLogo wrote %d %v", size, storage, err, c.NewSize, c.Storage)
		}
		t.Logf("replaced: volume %#x, file %#x, %d -> %d bytes, %s, %s", c.VolumeOffset, c.NewOffset, c.OldSize, c.NewSize, c.Placement, c.Storage)
		for _, r := range c.Recompressed {
			t.Logf("  recompressed: file %s at %#x, %d -> %d bytes, %s", r.File.GUID, r.NewOffset, r.File.Size, r.NewSize, r.Placement)
		}
	}
}

// fetchFirmware returns the firmware file of e, downloading and verifying the vendor's
// zip into cache when needed.
func fetchFirmware(cache string, e vendorFirmware) ([]byte, error) {
	zipPath := filepath.Join(cache, e.SHA256+".zip")
	if !fileHasHash(zipPath, e.SHA256) {
		if err := download(e.URL, zipPath); err != nil {
			return nil, err
		}
		if !fileHasHash(zipPath, e.SHA256) {
			os.Remove(zipPath)
			return nil, fmt.Errorf("%s: SHA-256 mismatch, the vendor file changed", e.URL)
		}
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if path.Base(f.Name) != e.File {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("%s not found in %s", e.File, e.URL)
}

func download(url, dst string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func fileHasHash(p, want string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == strings.ToLower(want)
}
