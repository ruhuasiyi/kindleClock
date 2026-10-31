package display

import (
	"image"
	"image/color"
	"testing"

	"github.com/rhsy/kindleclock/src/internal/render"
)

// marked 造一张 1448×1072 的图，四角各填不同灰度，
// 用来验证旋转方向与位置。
func marked() *image.Gray {
	img := image.NewGray(image.Rect(0, 0, render.CanvasW, render.CanvasH))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	// 左上角 10x10 = 黑
	fill(img, 0, 0, 10, 10, 0)
	// 右上角 = 灰 50
	fill(img, render.CanvasW-10, 0, 10, 10, 50)
	// 左下角 = 灰 100
	fill(img, 0, render.CanvasH-10, 10, 10, 100)
	// 右下角 = 灰 150
	fill(img, render.CanvasW-10, render.CanvasH-10, 10, 10, 150)
	return img
}

func fill(img *image.Gray, x, y, w, h int, v uint8) {
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			img.SetGray(px, py, color.Gray{Y: v})
		}
	}
}

func TestRotateCWDimensions(t *testing.T) {
	out := RotateCW(marked())
	b := out.Bounds()
	if b.Dx() != render.CanvasH || b.Dy() != render.CanvasW {
		t.Fatalf("顺时针旋转后应为 %dx%d，实际 %dx%d",
			render.CanvasH, render.CanvasW, b.Dx(), b.Dy())
	}
}

func TestRotateCCWDimensions(t *testing.T) {
	out := RotateCCW(marked())
	b := out.Bounds()
	if b.Dx() != render.CanvasH || b.Dy() != render.CanvasW {
		t.Fatalf("逆时针旋转后应为 %dx%d，实际 %dx%d",
			render.CanvasH, render.CanvasW, b.Dx(), b.Dy())
	}
}

// 顺时针：横版左上角 → 竖版右上角
func TestRotateCWCornerMapping(t *testing.T) {
	out := RotateCW(marked())
	W := render.CanvasH // 竖版宽 = 1072

	// 横版 (0,0) 黑 → 竖版 (W-1, 0)
	if got := out.GrayAt(W-1, 0).Y; got != 0 {
		t.Errorf("横版左上角应映射到竖版右上角，得到灰度 %d", got)
	}
	// 横版右上 (CanvasW-1, 0) 灰 50 → 竖版右下 (W-1, CanvasW-1)
	if got := out.GrayAt(W-1, render.CanvasW-1).Y; got != 50 {
		t.Errorf("横版右上角应映射到竖版右下角，得到 %d", got)
	}
	// 横版左下 → 竖版左上
	if got := out.GrayAt(0, 0).Y; got != 100 {
		t.Errorf("横版左下角应映射到竖版左上角，得到 %d", got)
	}
	// 横版右下 → 竖版左下
	if got := out.GrayAt(0, render.CanvasW-1).Y; got != 150 {
		t.Errorf("横版右下角应映射到竖版左下角，得到 %d", got)
	}
}

// 逆时针：横版左上角 → 竖版左下角
func TestRotateCCWCornerMapping(t *testing.T) {
	out := RotateCCW(marked())

	if got := out.GrayAt(0, render.CanvasW-1).Y; got != 0 {
		t.Errorf("横版左上角应映射到竖版左下角，得到 %d", got)
	}
	if got := out.GrayAt(0, 0).Y; got != 50 {
		t.Errorf("横版右上角应映射到竖版左上角，得到 %d", got)
	}
	if got := out.GrayAt(render.CanvasH-1, render.CanvasW-1).Y; got != 100 {
		t.Errorf("横版左下角应映射到竖版右下角，得到 %d", got)
	}
	if got := out.GrayAt(render.CanvasH-1, 0).Y; got != 150 {
		t.Errorf("横版右下角应映射到竖版右上角，得到 %d", got)
	}
}

func TestRotateRoundTrip(t *testing.T) {
	src := marked()
	back := RotateCCW(RotateCW(src))
	if back.Bounds() != src.Bounds() {
		t.Fatalf("往返后尺寸不一致")
	}
	for i := range src.Pix {
		if src.Pix[i] != back.Pix[i] {
			t.Fatalf("顺时针再逆时针应还原原图，第 %d 像素不同", i)
		}
	}
}

func TestRotateRejectsBadDir(t *testing.T) {
	if _, err := Rotate(marked(), "right"); err == nil {
		t.Fatal("非法方向应报错")
	}
	if _, err := Rotate(marked(), "cw"); err != nil {
		t.Errorf("cw 应合法: %v", err)
	}
	if _, err := Rotate(marked(), "ccw"); err != nil {
		t.Errorf("ccw 应合法: %v", err)
	}
}

// 矩形映射必须与像素映射一致：随机取矩形，检查其四角像素在目标图中的位置。
func TestMapRectCWMatchesPixelMapping(t *testing.T) {
	r := render.Rect{X: 100, Y: 200, W: 300, H: 150}
	got := MapRectCW(r)

	wantX := render.CanvasH - r.Y - r.H
	wantY := r.X
	if got.X != wantX || got.Y != wantY || got.W != r.H || got.H != r.W {
		t.Errorf("MapRectCW = %+v，期望 X=%d Y=%d W=%d H=%d",
			got, wantX, wantY, r.H, r.W)
	}
}

func TestMapRectCCWMatchesPixelMapping(t *testing.T) {
	r := render.Rect{X: 100, Y: 200, W: 300, H: 150}
	got := MapRectCCW(r)

	wantX := r.Y
	wantY := render.CanvasW - r.X - r.W
	if got.X != wantX || got.Y != wantY || got.W != r.H || got.H != r.W {
		t.Errorf("MapRectCCW = %+v，期望 X=%d Y=%d W=%d H=%d",
			got, wantX, wantY, r.H, r.W)
	}
}

func TestMapRegionsCWFitsPortrait(t *testing.T) {
	for _, r := range MapRegionsCW(render.Regions()) {
		if r.Rect.X < 0 || r.Rect.Y < 0 ||
			r.Rect.Right() > render.CanvasH || r.Rect.Bottom() > render.CanvasW {
			t.Errorf("区域 %s 映射后越界: %+v", r.ID, r.Rect)
		}
	}
}

func TestMapRegionsCCWFitsPortrait(t *testing.T) {
	for _, r := range MapRegionsCCW(render.Regions()) {
		if r.Rect.X < 0 || r.Rect.Y < 0 ||
			r.Rect.Right() > render.CanvasH || r.Rect.Bottom() > render.CanvasW {
			t.Errorf("区域 %s 映射后越界: %+v", r.ID, r.Rect)
		}
	}
}

func TestMapRegionsPreservesIDsAndWaveforms(t *testing.T) {
	src := render.Regions()
	for _, dir := range [][]render.Region{MapRegionsCW(src), MapRegionsCCW(src)} {
		if len(dir) != len(src) {
			t.Fatalf("区域数变化: %d vs %d", len(dir), len(src))
		}
		for i := range src {
			if dir[i].ID != src[i].ID || dir[i].Waveform != src[i].Waveform {
				t.Errorf("第 %d 个区域的 ID/波形被改动", i)
			}
		}
	}
}

// 关键不变量：在横版里互不重叠的区域，映射后仍然互不重叠。
func TestMapRegionsStayDisjoint(t *testing.T) {
	for _, set := range [][]render.Region{MapRegionsCW(render.Regions()), MapRegionsCCW(render.Regions())} {
		for i := 0; i < len(set); i++ {
			for j := i + 1; j < len(set); j++ {
				if set[i].Rect.Standard().Overlaps(set[j].Rect.Standard()) {
					t.Errorf("%s 与 %s 映射后重叠", set[i].ID, set[j].ID)
				}
			}
		}
	}
}
