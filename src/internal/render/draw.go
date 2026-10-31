package render

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// MeasureString 返回字符串的像素宽度（四舍五入）。
//
// 缺字形的处理很关键：中文字体通常不含拉丁字母以外的缺字，而回退字体
// （gofont）不含中文。若缺字形就跳过，中文的测量宽度会是 0，
// 导致 Ellipsize 认为「不超宽」而永不截断 —— 界面上的长课程名会溢出到
// 教室名上。
//
// 注意 opentype.Face.GlyphAdvance 在缺字形时通常仍返回 .notdef 的有效推进
// （gofont 与 STHeiti 都是 0.75em），那个值才是实际绘制出来的宽度，必须留着。
// 只有当它连 0 都返回时，才用 ascent/2 兜底 —— 这已经小于任何真实字形，
// 宁可低估也不能让宽度凭空消失。
func MeasureString(f font.Face, s string) int {
	var w fixed.Int26_6
	// 缺字且推进为 0 时的兜底：ascent 的一半（gofont 24px 下约 11.5px）
	fallback := f.Metrics().Ascent / 2
	for _, r := range s {
		adv, ok := f.GlyphAdvance(r)
		if !ok && adv == 0 {
			adv = fallback
		}
		w += adv
	}
	return int((w + 32) >> 6)
}

// Text 以 (x, y) 为基线起点左对齐绘制。
// 参数用 draw.Image 是为了直接喂给 font.Drawer。
func Text(img draw.Image, f font.Face, s string, x, y int, c color.Color) {
	if s == "" {
		return
	}
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: f,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

// TextCentered 在 [x, x+w) 内水平居中。
func TextCentered(img draw.Image, f font.Face, s string, x, y, w int, c color.Color) {
	tw := MeasureString(f, s)
	Text(img, f, s, x+(w-tw)/2, y, c)
}

// TextRight 让字符串的右边缘落在 right。
func TextRight(img draw.Image, f font.Face, s string, right, y int, c color.Color) {
	tw := MeasureString(f, s)
	Text(img, f, s, right-tw, y, c)
}

// TextInkCentered 按**墨迹包围盒**在 [x, x+w) 内水平居中，而不是按 advance。
//
// 为什么需要它：TextCentered 按字形的 advance 居中，但数字的墨迹在 advance
// 里并不对称。实测 STHeitiBold@200px 的「1」advance 111px、墨迹只占
// [19.2, 70.0]，墨迹中心比 advance 中心偏左 10.9px。用 TextCentered 时，
// 分钟从 :42 跳到 :12，「1」会跳 11px 再回来 —— 这正是把 DigitCellW
// 从 96 改到 118 要消除的「数字左右挪动」。
func TextInkCentered(img draw.Image, f font.Face, s string, x, y, w int, c color.Color) {
	if s == "" {
		return
	}
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: f, Dot: fixed.P(0, y)}

	// 先量出整串的墨迹范围（相对笔位 0）
	minX, maxX := 1<<30, -1<<30
	pen := fixed.Int26_6(0)
	for _, r := range s {
		b, adv, ok := d.Face.GlyphBounds(r)
		if ok {
			l := pen + b.Min.X
			rt := pen + b.Max.X
			if int(l) < minX {
				minX = int(l)
			}
			if int(rt) > maxX {
				maxX = int(rt)
			}
		}
		pen += adv
	}
	if maxX <= minX {
		// 没有可用墨迹信息（缺字形），退回 advance 居中
		TextCentered(img, f, s, x, y, w, c)
		return
	}
	inkMin := int((fixed.Int26_6(minX) + 32) >> 6)
	inkMax := int((fixed.Int26_6(maxX) + 32) >> 6)
	inkW := inkMax - inkMin
	d.Dot = fixed.P(x+(w-inkW)/2-inkMin, y)
	d.DrawString(s)
}

// Ellipsize 把 s 截断到不超过 maxWidth 像素，末尾加 …。
//
// 逐 rune 退让，保证中文等多字节字符不会被从中间劈开。
func Ellipsize(f font.Face, s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if MeasureString(f, s) <= maxWidth {
		return s
	}
	const ellipsis = "…"
	ellW := MeasureString(f, ellipsis)
	if ellW > maxWidth {
		return ellipsis
	}
	runes := []rune(s)
	for n := len(runes) - 1; n >= 0; n-- {
		cand := string(runes[:n]) + ellipsis
		if MeasureString(f, cand) <= maxWidth {
			return cand
		}
	}
	return ellipsis
}

// TextColumnLeft 以 x 为**墨迹左缘**起点绘制，用于贴着刷新区左边界起画的文本。
//
// 为什么不用 Text：部分字体的首字形有负左边距（STHeiti 的「2」实测为 −1px），
// 按 x 起画时墨迹会落在 x−1。若 x 正好是刷新区的左边界，那一列像素就落进
// 「不属于任何族新区域」的盲带，永远不会被刷新 —— 真机上的表现是一条洗不掉的
// 残影。这里把笔位右移 |Min.X|，保证墨迹不左越 x。
func TextColumnLeft(img draw.Image, f font.Face, s string, x, y int, c color.Color) {
	if s == "" {
		return
	}
	d := &font.Drawer{Face: f}
	b, _, ok := d.Face.GlyphBounds([]rune(s)[0])
	if ok && b.Min.X < 0 {
		x += int((-b.Min.X + 63) >> 6)
	}
	Text(img, f, s, x, y, c)
}

// FillRect 填充矩形。右下为开区间。
//
// 先用 Intersect 把矩形夹到图像边界内：这不是正确性所需（SetGray 自带
// bounds 检查，越界也不会 panic），而是性能优化 —— 越界的大矩形若不裁剪，
// 内层循环会空转上亿次（20000×20000 的病态矩形实测 237ms → 108µs）。
// 别当冗余删掉。
func FillRect(img *image.Gray, x, y, w, h int, c color.Color) {
	g := toGray(c)
	r := image.Rect(x, y, x+w, y+h).Intersect(img.Bounds())
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			img.SetGray(px, py, g)
		}
	}
}

// FillRoundRect 填充圆角矩形。半径大于短边一半时自动收敛为胶囊形。
func FillRoundRect(img *image.Gray, x, y, w, h, radius int, c color.Color) {
	if radius < 0 {
		radius = 0
	}
	if maxR := min(w, h) / 2; radius > maxR {
		radius = maxR
	}
	g := toGray(c)

	// 中间主体
	FillRect(img, x, y+radius, w, h-2*radius, c)
	// 上下的横带（去掉四角）
	FillRect(img, x+radius, y, w-2*radius, radius, c)
	FillRect(img, x+radius, y+h-radius, w-2*radius, radius, c)

	// 四角按圆逐个判断
	r2 := radius * radius
	corners := []struct {
		cx, cy int // 圆心
		dx, dy int // 象限方向
	}{
		{x + radius, y + radius, -1, -1},
		{x + w - radius - 1, y + radius, 1, -1},
		{x + radius, y + h - radius - 1, -1, 1},
		{x + w - radius - 1, y + h - radius - 1, 1, 1},
	}
	for _, cn := range corners {
		for dy := 0; dy < radius; dy++ {
			for dx := 0; dx < radius; dx++ {
				if dx*dx+dy*dy <= r2 {
					px := cn.cx + cn.dx*dx
					py := cn.cy + cn.dy*dy
					if image.Pt(px, py).In(img.Bounds()) {
						img.SetGray(px, py, g)
					}
				}
			}
		}
	}
}

// strokeRoundRect 画圆角矩形的描边，thickness 为环带宽度。
//
// 只写环带上的像素，内部一个都不碰 —— 所以「描边是否在数字之前画」不再
// 影响正确性。用「填外圈再填内圈白块」实现的话，内圈白块会擦掉已经画好的
// 数字，调用顺序稍有不慎就会出现「选中日期没有日号」。
func strokeRoundRect(img *image.Gray, x, y, w, h, radius, thickness int, c color.Color) {
	if thickness <= 0 || w <= 0 || h <= 0 {
		return
	}
	if radius < 0 {
		radius = 0
	}
	if maxR := min(w, h) / 2; radius > maxR {
		radius = maxR
	}
	innerX, innerY := x+thickness, y+thickness
	innerW, innerH := w-2*thickness, h-2*thickness
	innerR := max(radius-thickness, 0)
	hasInner := innerW > 0 && innerH > 0

	g := toGray(c)
	r := image.Rect(x, y, x+w, y+h).Intersect(img.Bounds())
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			if !inRoundRect(px, py, x, y, w, h, radius) {
				continue
			}
			if hasInner && inRoundRect(px, py, innerX, innerY, innerW, innerH, innerR) {
				continue
			}
			img.SetGray(px, py, g)
		}
	}
}

// inRoundRect 判断像素是否落在圆角矩形内。角心与 FillRoundRect 一致：
// 左上 (x+radius, y+radius)、右下 (x+w-radius-1, y+h-radius-1)。
func inRoundRect(px, py, x, y, w, h, radius int) bool {
	if px < x || px >= x+w || py < y || py >= y+h {
		return false
	}
	if radius <= 0 {
		return true
	}
	cx := px
	switch {
	case px < x+radius:
		cx = x + radius
	case px >= x+w-radius:
		cx = x + w - radius - 1
	}
	cy := py
	switch {
	case py < y+radius:
		cy = y + radius
	case py >= y+h-radius:
		cy = y + h - radius - 1
	}
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= radius*radius
}

// HLine 画 1px 水平线。
func HLine(img *image.Gray, x, y, w int, c color.Color) {
	FillRect(img, x, y, w, 1, c)
}

// VLine 画 1px 垂直线。
func VLine(img *image.Gray, x, y, h int, c color.Color) {
	FillRect(img, x, y, 1, h, c)
}

// Circle 画实心圆。
func Circle(img *image.Gray, cx, cy, r int, c color.Color) {
	g := toGray(c)
	r2 := r * r
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r2 {
				px, py := cx+dx, cy+dy
				if image.Pt(px, py).In(img.Bounds()) {
					img.SetGray(px, py, g)
				}
			}
		}
	}
}

func toGray(c color.Color) color.Gray {
	r, g, b, _ := c.RGBA()
	// 单色渲染：取亮度
	y := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	y = y / 257 // 16 位 → 8 位
	v := uint8(math.Round(y))
	return color.Gray{Y: v}
}
