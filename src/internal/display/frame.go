package display

import (
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"

	"github.com/rhsy/kindleclock/src/internal/render"
)

// 竖版 framebuffer 的几何：逻辑尺寸 1072×1448，行跨度 1088（右侧 16 列是
// 硬件填充，fbgrab 会裁掉）。实测（PW3 / 5.15.1.1）直接读 /dev/fb0 得到
// 的字节与 fbgrab 输出的 PNG 逐像素完全一致，所以不需要起子进程截图。
const (
	portraitW = render.CanvasH // 1072
	portraitH = render.CanvasW // 1448
)

// ReadVisibleFrame 读一次当前显示中的画面，返回裁剪后的竖版灰度图。
//
// 为什么需要它：电子墨水屏上「我们画了什么」和「屏幕显示什么」是两回事。
// Kindle 的原生界面、KUAL 的提示、框架重启都可能把屏幕盖掉，而我们的
// 脏区比对只知道自己上一帧画了什么 —— 盖掉之后它认为一切正常，
// 于是只有时钟区还在更新，其余部分永远停在别人的画面上。
//
// 实测读一次 1.5 MB 约 10 ms，每分钟一次可以忽略。
func ReadVisibleFrame(fbPath, sysDir string) (*image.Gray, error) {
	stride, err := sysInt(sysDir + "/stride")
	if err != nil {
		return nil, fmt.Errorf("读 framebuffer 行跨度: %w", err)
	}
	bpp, err := sysInt(sysDir + "/bits_per_pixel")
	if err != nil {
		return nil, fmt.Errorf("读 framebuffer 位深: %w", err)
	}
	if bpp != 8 {
		return nil, fmt.Errorf("framebuffer 位深是 %d，只支持 8", bpp)
	}
	if stride < portraitW {
		return nil, fmt.Errorf("行跨度 %d 小于屏宽 %d", stride, portraitW)
	}

	f, err := os.Open(fbPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, stride*portraitH)
	if _, err := f.ReadAt(buf, 0); err != nil {
		// ReadAt 在短读时返回 io.EOF，这里视为失败：画面不全就没什么可比的。
		return nil, fmt.Errorf("读 %s: %w", fbPath, err)
	}

	img := image.NewGray(image.Rect(0, 0, portraitW, portraitH))
	for y := 0; y < portraitH; y++ {
		copy(img.Pix[y*portraitW:(y+1)*portraitW], buf[y*stride:y*stride+portraitW])
	}
	return img, nil
}

// InkMismatchRatio 检查「我们画过东西的地方」还在不在。
//
// 为什么不能比「整屏差异像素占比」：我们的画布绝大部分是白的（墨迹只占
// 百分之几），所以哪怕整屏被别人盖掉，差异占比也只有 5% 左右 —— 阈值定多少
// 都不对。真正的判据是：**我们画的那些笔画还在不在**。
//
// 只统计 want 上非白的采样点（我们画了东西的地方），看屏幕在那些点上
// 是否还是同一个灰度。返回值是「消失了的笔画」占比。
//
// 反过来（屏幕上有东西、我们这里是白的）不计入：e-ink 残影和原生界面的
// 状态栏都会造成这种情况，那不说明我们的画面被盖了。
//
// 采样到的笔画点太少时返回 0：样本不足就不下结论。
func InkMismatchRatio(screen, want *image.Gray, step int) float64 {
	if step < 1 {
		step = 1
	}
	w := screen.Bounds().Dx()
	h := screen.Bounds().Dy()
	if want.Bounds().Dx() != w || want.Bounds().Dy() != h {
		return 1
	}

	const (
		// 我们认为「画过东西」的灰度上限。留一点余量，让浅灰的次要文字
		// （ColorLight = 179）也算进来。
		inkMax = 230
		// 允许的灰度误差：局部刷新会留下轻微残影，那不算被盖。
		tolerance = 24
		// 至少要采到这么多个笔画点才下结论。
		minSamples = 64
	)

	var inkSamples, missing int
	for y := 0; y < h; y += step {
		row := y * w
		for x := 0; x < w; x += step {
			wantV := int(want.Pix[row+x])
			if wantV > inkMax {
				continue // 这里是白的，我们没画东西，不参与判断
			}
			inkSamples++
			gotV := int(screen.Pix[row+x])
			if d := gotV - wantV; d < -tolerance || d > tolerance {
				missing++
			}
		}
	}
	if inkSamples < minSamples {
		return 0
	}
	return float64(missing) / float64(inkSamples)
}

func sysInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}
