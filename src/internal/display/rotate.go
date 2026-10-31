// Package display 负责把渲染好的横版位图送上 Kindle 的 framebuffer。
//
// 设备的 /dev/fb0 是竖版 1072×1448（实测 rotation: 3）。所以这里先做
// 90° 旋转，再按区域切图，最后调用 fbink 的局部刷新。
//
// 本包不感知"时钟""课程"这些概念，只处理位图与矩形。
package display

import (
	"fmt"
	"image"

	"github.com/rhsy/kindleclock/src/internal/render"
)

// RotateCW 顺时针旋转 90°。
//
// 映射：源 (x,y) → 目标 (dstX, dstY) = (H-1-y, x)，H = 源高度。
// 直觉校验：源左上角 (0,0) 转到目标右上角 (H-1, 0)。
//
// **尺寸必须取自 src.Bounds()，不能写死 1448×1072** —— 否则
// RotateCCW(RotateCW(img)) 这种往返调用会按错的尺寸重排，结果错乱。
func RotateCW(src *image.Gray) *image.Gray {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewGray(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		srcRow := y * src.Stride
		for x := 0; x < w; x++ {
			dx := h - 1 - y
			dy := x
			dst.Pix[dy*dst.Stride+dx] = src.Pix[srcRow+x]
		}
	}
	return dst
}

// RotateCCW 逆时针旋转 90°。
//
// 映射：源 (x,y) → 目标 (dstX, dstY) = (y, W-1-x)，W = 源宽度。
// 直觉校验：源左上角 (0,0) 转到目标左下角 (0, W-1)。
func RotateCCW(src *image.Gray) *image.Gray {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewGray(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		srcRow := y * src.Stride
		for x := 0; x < w; x++ {
			dx := y
			dy := w - 1 - x
			dst.Pix[dy*dst.Stride+dx] = src.Pix[srcRow+x]
		}
	}
	return dst
}

// Rotate 按方向字符串旋转。
func Rotate(src *image.Gray, dir string) (*image.Gray, error) {
	switch dir {
	case "cw":
		return RotateCW(src), nil
	case "ccw":
		return RotateCCW(src), nil
	default:
		return nil, fmt.Errorf("rotate 必须是 cw 或 ccw，实际 %q", dir)
	}
}

// MapRectCW 把横版矩形映射到顺时针旋转后的竖版坐标。
//
// 横版矩形 (x, y, w, h) → 竖版 left = H - y - h, top = x, width = h, height = w
func MapRectCW(r render.Rect) render.Rect {
	return render.Rect{
		X: render.CanvasH - r.Y - r.H,
		Y: r.X,
		W: r.H,
		H: r.W,
	}
}

// MapRectCCW 把横版矩形映射到逆时针旋转后的竖版坐标。
//
// 横版矩形 (x, y, w, h) → 竖版 left = y, top = W - x - w, width = h, height = w
func MapRectCCW(r render.Rect) render.Rect {
	return render.Rect{
		X: r.Y,
		Y: render.CanvasW - r.X - r.W,
		W: r.H,
		H: r.W,
	}
}

// MapRegionsCW 批量映射，保留 ID 与波形。
func MapRegionsCW(rs []render.Region) []render.Region {
	out := make([]render.Region, len(rs))
	for i, r := range rs {
		out[i] = render.Region{ID: r.ID, Rect: MapRectCW(r.Rect), Waveform: r.Waveform}
	}
	return out
}

// MapRegionsCCW 批量映射，保留 ID 与波形。
func MapRegionsCCW(rs []render.Region) []render.Region {
	out := make([]render.Region, len(rs))
	for i, r := range rs {
		out[i] = render.Region{ID: r.ID, Rect: MapRectCCW(r.Rect), Waveform: r.Waveform}
	}
	return out
}

// MapRegions 按方向批量映射。
func MapRegions(rs []render.Region, dir string) ([]render.Region, error) {
	switch dir {
	case "cw":
		return MapRegionsCW(rs), nil
	case "ccw":
		return MapRegionsCCW(rs), nil
	default:
		return nil, fmt.Errorf("rotate 必须是 cw 或 ccw，实际 %q", dir)
	}
}
