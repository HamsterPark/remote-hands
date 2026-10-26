package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/user"
	"path/filepath"
	"sync"
)

type sysInfo struct {
	os, user, home, desktop string
}

var getSysInfo = sync.OnceValue(func() sysInfo {
	si := sysInfo{os: osDescription()}
	si.home, _ = os.UserHomeDir()
	if u, err := user.Current(); err == nil {
		si.user = u.Username
	}
	si.user = firstNonEmpty(si.user, os.Getenv("USERNAME"), os.Getenv("USER"), "unknown")
	si.desktop = desktopDir(si.home)
	return si
})

// homeDesktop is the fallback Desktop folder: ~/Desktop if it exists.
func homeDesktop(home string) string {
	d := filepath.Join(home, "Desktop")
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return d
	}
	return ""
}

// maxShotSide matches the largest image size Claude looks at without
// scaling it down again.
const maxShotSide = 1568

// takeScreenshots captures every screen and returns them as JPEG images.
func takeScreenshots(ctx context.Context) ([]content, error) {
	dir, err := os.MkdirTemp("", "remote-hands-shot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	files, err := captureScreens(ctx, dir)
	if err != nil {
		return nil, err
	}
	var out []content
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("reading the screenshot: %w", err)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, downscale(img, maxShotSide), &jpeg.Options{Quality: 80}); err != nil {
			return nil, err
		}
		out = append(out, content{Type: "image", Data: base64.StdEncoding.EncodeToString(buf.Bytes()), MimeType: "image/jpeg"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no screenshot was produced")
	}
	return out, nil
}

// existingFiles returns the paths that exist and are not empty.
func existingFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			out = append(out, p)
		}
	}
	return out
}

// downscale shrinks img so that neither side exceeds maxSide, averaging the
// source pixels that fall into each target pixel (keeps text readable).
func downscale(img image.Image, maxSide int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxSide && h <= maxSide {
		return img
	}
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Rect, img, b.Min, draw.Src)
	scale := float64(maxSide) / float64(max(w, h))
	nw, nh := max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := y*h/nh, max((y+1)*h/nh, y*h/nh+1)
		for x := 0; x < nw; x++ {
			x0, x1 := x*w/nw, max((x+1)*w/nw, x*w/nw+1)
			var r, g, bl, n uint32
			for sy := y0; sy < y1; sy++ {
				i := sy*src.Stride + x0*4
				for sx := x0; sx < x1; sx++ {
					r += uint32(src.Pix[i])
					g += uint32(src.Pix[i+1])
					bl += uint32(src.Pix[i+2])
					i += 4
					n++
				}
			}
			j := y*dst.Stride + x*4
			dst.Pix[j], dst.Pix[j+1], dst.Pix[j+2], dst.Pix[j+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return dst
}
