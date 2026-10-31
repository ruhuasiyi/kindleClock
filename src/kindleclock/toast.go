package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"strings"

	"golang.org/x/image/font"

	"github.com/rhsy/kindleclock/src/internal/config"
	"github.com/rhsy/kindleclock/src/internal/display"
	"github.com/rhsy/kindleclock/src/internal/render"
)

// 提示文字的字号与留白。36px 是实测能在一行放下十几个汉字、又不至于
// 在竖版屏上显得局促的档位。
const (
	toastPx      = 36
	toastPadding = 28
)

// runToast 把一段文字画成图、按配置旋转成竖版、贴到屏幕正中，然后退出。
//
// 为什么不用 `fbink -m -M` 直接印字：
//
// 屏幕上的内容是先按横版 1448×1072 画好、再整体旋转成 1072×1448 竖版
// 交给 fbink 的。而 fbink 的 -m -M 按**竖版 framebuffer 坐标**画字 ——
// 在横放的设备上，提示文字会整个歪 90 度。
//
// 这不是推测：2026-10-30 在真机上跑 `status.sh` 截图确认，七行状态
// 全部竖着躺在表盘上。FBInk 1.25.0 的 --help 里没有任何旋转/方向参数
// （只有 -K --animate direction=，那是刷新动效，不是文字方向）。
//
// 所以只能借我们自己的渲染管线：把字画进一张小图，连旋转一起做掉，
// 再用 `fbink -g` 贴回去。
//
// 这个模式**不碰 flock** —— 它只往屏幕上贴一张小图，不参与主循环的
// 脏区状态。主循环下一帧如果发现某个区域变了，会把它盖掉，这正是想要的。
func runToast(o *options) error {
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return err
	}
	return renderToast(cfg, o.toastText)
}

// renderToast 抽出来是为了能脱离 fbink 测：只验证画布尺寸、旋转方向和墨迹。
func renderToast(cfg *config.Config, text string) error {
	fonts, err := render.NewFonts(cfg.Display.FontRegular, cfg.Display.FontBold)
	if err != nil {
		return err
	}
	defer fonts.Close()

	face, err := fonts.Face(render.Regular, toastPx)
	if err != nil {
		return err
	}

	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return fmt.Errorf("提示内容为空")
	}

	landscape := drawToastLandscape(face, lines)
	portrait, err := display.Rotate(landscape, cfg.Display.Rotate)
	if err != nil {
		return err
	}

	// 旋转后的图尺寸是 (h, w)，要居中贴到 1072×1448 的竖版屏上
	b := portrait.Bounds()
	rect := render.Rect{
		X: (render.CanvasH - b.Dx()) / 2,
		Y: (render.CanvasW - b.Dy()) / 2,
		W: b.Dx(),
		H: b.Dy(),
	}
	if rect.X < 0 {
		rect.X = 0
	}
	if rect.Y < 0 {
		rect.Y = 0
	}

	png := filepath.Join(cfg.Display.WorkDir, "toast.png")
	if err := display.WriteRegionPNG(portrait,
		render.Rect{X: 0, Y: 0, W: b.Dx(), H: b.Dy()}, png); err != nil {
		return err
	}

	fb := display.NewFbink(cfg.Display.Fbink)
	return fb.DrawRegion(context.Background(), png, rect, "GC16")
}

// drawToastLandscape 把多行文字画成一张白底黑字的横版小图。
func drawToastLandscape(face font.Face, lines []string) *image.Gray {
	ascent := face.Metrics().Ascent.Ceil()
	lineH := toastPx * 14 / 10

	widest := 0
	for _, ln := range lines {
		if w := render.MeasureString(face, ln); w > widest {
			widest = w
		}
	}

	// 宽度留够内边距；高度按行数。+1 行的高度给降部留余量。
	w := widest + 2*toastPadding
	h := lineH*len(lines) + 2*toastPadding

	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}

	for i, ln := range lines {
		baseline := toastPadding + ascent + i*lineH
		render.TextCentered(img, face, ln, 0, baseline, w, color.Black)
	}
	return img
}
