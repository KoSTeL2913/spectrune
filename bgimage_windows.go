// User-chosen background photo for the GUI's theme personalization — the
// picker opens via the same native GetOpenFileNameW dialog filedialog.go
// already uses, and the result comes back as a data: URI the JS side can
// drop straight into a CSS background-image, exactly like icons.go's
// getAppIconDataURI already does for app icons. No new "can WebView2
// load this URL scheme" question to answer — it's the same trick already
// proven working in this app.
package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
)

// maxBackgroundDim caps the longer side of a chosen photo — keeps the
// resulting data URI (and therefore what gets persisted to the webview's
// localStorage on the JS side) to a reasonable size regardless of how
// large the source photo is.
const maxBackgroundDim = 1600

func chooseBackgroundImage() (string, error) {
	path, err := browseForFile("Choose a background image", "Images (*.jpg;*.jpeg;*.png;*.gif)", "*.jpg;*.jpeg;*.png;*.gif")
	if err != nil || path == "" {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("decoding image: %w", err)
	}
	img = downscaleImage(img, maxBackgroundDim)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return "", fmt.Errorf("encoding image: %w", err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// downscaleImage shrinks img (nearest-neighbor — fine for a background
// photo behind a mostly-opaque card, no need to pull in a resampling
// library for this) so neither dimension exceeds max. Returns img
// unchanged if it's already small enough.
func downscaleImage(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	var newW, newH int
	if w >= h {
		newW = max
		newH = h * max / w
	} else {
		newH = max
		newW = w * max / h
	}
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			srcX := b.Min.X + x*w/newW
			srcY := b.Min.Y + y*h/newH
			dst.Set(x, y, img.At(srcX, srcY))
		}
	}
	return dst
}
