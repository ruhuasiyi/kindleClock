package render

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// testFace 用 Go 自带的 gofont，测试不依赖设备字体。
func testFace(t *testing.T, size float64) font.Face {
	t.Helper()
	f, err := opentype.Parse(testFontData())
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { face.Close() })
	return face
}

func white() *image.Gray {
	img := image.NewGray(image.Rect(0, 0, 400, 200))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

// isBlank 判断矩形区域是否全白
func isBlank(img *image.Gray, r image.Rectangle) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.GrayAt(x, y).Y != 255 {
				return false
			}
		}
	}
	return true
}

func TestFillRect(t *testing.T) {
	img := white()
	FillRect(img, 10, 20, 50, 30, color.Gray{0})
	if img.GrayAt(10, 20).Y != 0 {
		t.Error("左上角应为黑")
	}
	if img.GrayAt(59, 49).Y != 0 {
		t.Error("右下角应为黑")
	}
	if img.GrayAt(60, 50).Y != 255 {
		t.Error("矩形外应为白（右下开区间）")
	}
	if img.GrayAt(9, 20).Y != 255 {
		t.Error("左侧外应为白")
	}
}

func TestFillRectClips(t *testing.T) {
	img := white()
	// 越界绘制不应 panic
	FillRect(img, -10, -10, 50, 50, color.Gray{0})
	FillRect(img, 380, 180, 100, 100, color.Gray{0})
	if img.GrayAt(0, 0).Y != 0 {
		t.Error("裁剪后左上角应被涂黑")
	}
	if img.GrayAt(399, 199).Y != 0 {
		t.Error("裁剪后右下角应被涂黑")
	}
}

func TestHLine(t *testing.T) {
	img := white()
	HLine(img, 10, 50, 100, color.Gray{0})
	if img.GrayAt(10, 50).Y != 0 {
		t.Error("起点应为黑")
	}
	if img.GrayAt(109, 50).Y != 0 {
		t.Error("终点应为黑（右开区间内最后一个像素）")
	}
	if img.GrayAt(110, 50).Y != 255 {
		t.Error("超出一格应为白")
	}
	if img.GrayAt(10, 51).Y != 255 {
		t.Error("线只有 1px 高")
	}
}

func TestMeasureStringGrowsWithLength(t *testing.T) {
	f := testFace(t, 24)
	one := MeasureString(f, "1")
	two := MeasureString(f, "12")
	if two <= one {
		t.Errorf("两个字符 (%d) 应宽于一个 (%d)", two, one)
	}
	if MeasureString(f, "") != 0 {
		t.Error("空串宽度应为 0")
	}
}

func TestEllipsize(t *testing.T) {
	f := testFace(t, 24)
	// 用拉丁字符：gofont 不含中文字形，测宽度会走缺字兜底分支，
	// 那样测的就不是「截断」而是「兜底宽度」，掩盖真实行为。
	long := "This is a very long course name that must be truncated"
	short := "Math"

	if got := Ellipsize(f, short, 1000); got != short {
		t.Errorf("宽度充足时不应改动，得到 %q", got)
	}

	got := Ellipsize(f, long, 100)
	if got == long {
		t.Error("超宽时应被截断")
	}
	if !hasSuffix(got, "…") {
		t.Errorf("截断后应以 … 结尾，得到 %q", got)
	}
	if w := MeasureString(f, got); w > 100 {
		t.Errorf("截断后宽度 %d 仍超过 100", w)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func TestEllipsizeTinyWidth(t *testing.T) {
	f := testFace(t, 24)
	// 极窄时至少返回 "…" 或空，不能死循环
	got := Ellipsize(f, "content", 1)
	if got != "…" && got != "" {
		t.Errorf("极窄宽度应返回 … 或空，得到 %q", got)
	}
}

func TestTextDrawsPixels(t *testing.T) {
	f := testFace(t, 48)
	img := white()
	Text(img, f, "ABC", 10, 100, color.Gray{0})
	if isBlank(img, image.Rect(10, 40, 200, 110)) {
		t.Error("绘制文字后应有非白像素")
	}
}

func TestTextCentered(t *testing.T) {
	f := testFace(t, 32)
	img := white()
	TextCentered(img, f, "88", 0, 100, 400, color.Gray{0})

	// 文字应大致居中：左侧留白区不应有内容
	w := MeasureString(f, "88")
	leftEdge := (400 - w) / 2
	// 取左侧留白区的一段（避开字形可能的前导空白）
	if !isBlank(img, image.Rect(0, 40, leftEdge-8, 110)) {
		t.Error("居中后左侧留白区不应有内容")
	}
	// 右侧同样
	if !isBlank(img, image.Rect(leftEdge+w+8, 40, 400, 110)) {
		t.Error("居中后右侧留白区不应有内容")
	}
}

func TestTextRight(t *testing.T) {
	f := testFace(t, 32)
	img := white()
	TextRight(img, f, "END", 400, 100, color.Gray{0})
	// 右边界 400 附近应有内容
	if isBlank(img, image.Rect(300, 40, 400, 110)) {
		t.Error("右对齐后靠近右边界应有内容")
	}
}

// 按 advance 居中会让「1」偏左 11px：TextInkCentered 必须按墨迹居中。
func TestTextInkCentered(t *testing.T) {
	f := testFace(t, 64)
	for _, s := range []string{"1", "7", "0", "4"} {
		img := white()
		TextInkCentered(img, f, s, 20, 150, 200, color.Gray{0})

		// 墨迹包围盒
		minX, maxX := 1<<30, -1
		for y := 0; y < 200; y++ {
			for x := 0; x < 400; x++ {
				if img.GrayAt(x, y).Y != 255 {
					if x < minX {
						minX = x
					}
					if x > maxX {
						maxX = x
					}
				}
			}
		}
		if maxX < 0 {
			t.Fatalf("%q 没有画出墨迹", s)
		}
		inkCenter := (minX + maxX) / 2
		cellCenter := 20 + 200/2
		if d := inkCenter - cellCenter; d < -1 || d > 1 {
			t.Errorf("%q 墨迹中心 %d 偏离格中心 %d 达 %d px（应按墨迹居中，不是 advance）",
				s, inkCenter, cellCenter, d)
		}
	}
}

func TestTextInkCenteredEmpty(t *testing.T) {
	f := testFace(t, 32)
	img := white()
	TextInkCentered(img, f, "", 0, 100, 200, color.Gray{0})
	if !isBlank(img, image.Rect(0, 0, 400, 200)) {
		t.Error("空串不应画出任何像素")
	}
}

func TestFillRoundRect(t *testing.T) {
	img := white()
	FillRoundRect(img, 50, 50, 100, 60, 10, color.Gray{0})
	if img.GrayAt(100, 80).Y != 0 {
		t.Error("中心应为黑")
	}
	// 四角应被圆角裁掉（白色）
	if img.GrayAt(50, 50).Y != 255 {
		t.Error("左上角应因圆角而为白")
	}
}

// gofont 不含中文字形，正好用来测缺字兜底。
// 若兜底写错（跳过或缺省为 0），MeasureString 会得 0，
// Ellipsize 就会认为「不超宽」而永不截断 —— 真机上长课程名会溢出。
func TestMeasureStringFallbackForMissingGlyphs(t *testing.T) {
	f := testFace(t, 24)

	cjk := "高等数学"
	w := MeasureString(f, cjk)
	if w == 0 {
		t.Fatal("缺字形时 MeasureString 返回 0 —— 兜底失效")
	}
	// MeasureString 必须与实际绘制宽度一致。font.Drawer.MeasureString
	// 直接累加 GlyphAdvance（不看 ok），正是 font.Drawer 真正用的推进量，
	// 也就是「画出来的宽度」的权威值。允许 1px 的四舍五入差。
	// 若兜底错误地覆盖了缺字形返回的有效推进（0.75em → 0.48em），
	// 这里会立刻暴露。
	d := &font.Drawer{Face: f}
	for _, s := range []string{cjk, "Math 高等数学 （下）", "A1 中文"} {
		want := int((d.MeasureString(s) + 32) >> 6)
		if got := MeasureString(f, s); got < want-1 || got > want+1 {
			t.Errorf("MeasureString(%q)=%d，实际绘制宽度 %d", s, got, want)
		}
	}

	// Ellipsize 结果必须真的不超过 maxWidth
	const maxW = 60
	got := Ellipsize(f, "高等数学（下）与线性代数", maxW)
	if w2 := MeasureString(f, got); w2 > maxW {
		t.Errorf("截断后宽度 %d 仍超过 %d（得到 %q）", w2, maxW, got)
	}
}

func TestVLine(t *testing.T) {
	img := white()
	VLine(img, 50, 10, 100, color.Gray{0})
	if img.GrayAt(50, 10).Y != 0 {
		t.Error("起点应为黑")
	}
	if img.GrayAt(50, 109).Y != 0 {
		t.Error("终点应为黑（高 100，y 10..109）")
	}
	if img.GrayAt(50, 110).Y != 255 {
		t.Error("超出一格应为白")
	}
	if img.GrayAt(51, 10).Y != 255 {
		t.Error("竖线只有 1px 宽")
	}
}

func TestCircle(t *testing.T) {
	img := white()
	Circle(img, 100, 100, 30, color.Gray{0})
	if img.GrayAt(100, 100).Y != 0 {
		t.Error("圆心应为黑")
	}
	if img.GrayAt(100+25, 100).Y != 0 {
		t.Error("半径内（25 < 30）应为黑")
	}
	if img.GrayAt(100+35, 100).Y != 255 {
		t.Error("半径外（35 > 30）应为白 —— 圆被缩小了")
	}
	if img.GrayAt(100, 100+35).Y != 255 {
		t.Error("垂直方向半径外应为白")
	}
}

// FillRoundRect 的四个角都必须被裁掉，不能只裁左上角
func TestFillRoundRectAllFourCorners(t *testing.T) {
	img := white()
	const x, y, w, h, rad = 50, 50, 100, 60, 10
	FillRoundRect(img, x, y, w, h, rad, color.Gray{0})

	corners := []struct {
		name   string
		px, py int
	}{
		{"左上", x, y},
		{"右上", x + w - 1, y},
		{"左下", x, y + h - 1},
		{"右下", x + w - 1, y + h - 1},
	}
	for _, c := range corners {
		if got := img.GrayAt(c.px, c.py).Y; got != 255 {
			t.Errorf("%s角 (%d,%d) 应为白（被圆角裁掉），得到 %d", c.name, c.px, c.py, got)
		}
	}
	// 四边的中点必须在（证明只裁了角）
	edges := []struct {
		name   string
		px, py int
	}{
		{"上边中", x + w/2, y},
		{"下边中", x + w/2, y + h - 1},
		{"左边中", x, y + h/2},
		{"右边中", x + w - 1, y + h/2},
	}
	for _, e := range edges {
		if got := img.GrayAt(e.px, e.py).Y; got != 0 {
			t.Errorf("%s (%d,%d) 应为黑，得到 %d", e.name, e.px, e.py, got)
		}
	}

	// 四段圆弧本身必须被填上：这些点位于角落、但落在圆内，
	// 三条矩形带都覆盖不到，只有各自的角循环能画到。
	// 只断言 (x,y) 那样的极端角点是不够的 —— 那些点本来就该是白的，
	// 即使整段角循环被删掉也仍为白。
	arcs := []struct {
		name   string
		px, py int
	}{
		{"左上弧", x + rad - 1, y + 1},
		{"右上弧", x + w - rad, y + 1},
		{"左下弧", x + rad - 1, y + h - 2},
		{"右下弧", x + w - rad, y + h - 2},
	}
	for _, a := range arcs {
		if got := img.GrayAt(a.px, a.py).Y; got != 0 {
			t.Errorf("%s (%d,%d) 应被圆角弧填黑，得到 %d", a.name, a.px, a.py, got)
		}
	}
}

func TestFontsFaceIsCached(t *testing.T) {
	f, err := NewFontsFromData(testFontData(), testFontData())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	a, err := f.Face(Regular, 24)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Face(Regular, 24)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("相同 (Style, px) 必须命中缓存返回同一个字面")
	}

	// 不同字重必须是不同字面
	c, err := f.Face(Bold, 24)
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Error("不同字重不应返回同一字面")
	}
}

// 设备字体的冒烟检查。字体不在（比如 CI）就跳过。
func TestDeviceFontsSmoke(t *testing.T) {
	const reg = "/usr/java/lib/fonts/STHeitiMedium.ttf"
	const bold = "/usr/java/lib/fonts/STHeitiBold.ttf"
	if _, err := os.Stat(reg); err != nil {
		t.Skip("本机没有设备字体")
	}
	f, err := NewFonts(reg, bold)
	if err != nil {
		t.Fatalf("设备字体解析失败: %v", err)
	}
	defer f.Close()

	face, err := f.Face(Regular, 48)
	if err != nil {
		t.Fatal(err)
	}
	if w := MeasureString(face, "晴"); w == 0 {
		t.Error("中文字形测宽为 0 —— 设备字体不可用")
	}
}

// NewFonts 读磁盘路径这一支（设备字体不在时也能覆盖）。
func TestNewFontsFromFiles(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "reg.ttf")
	bold := filepath.Join(dir, "bold.ttf")
	for _, p := range []string{reg, bold} {
		if err := os.WriteFile(p, testFontData(), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	f, err := NewFonts(reg, bold)
	if err != nil {
		t.Fatal(err)
	}
	face, err := f.Face(Regular, 24)
	if err != nil {
		t.Fatal(err)
	}
	if MeasureString(face, "A") == 0 {
		t.Error("从文件加载的字面测宽为 0")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// 缺文件必须报错，而不是静默退化
	if _, err := NewFonts(filepath.Join(dir, "nope.ttf"), bold); err == nil {
		t.Error("常规字体不存在时应报错")
	}
	if _, err := NewFonts(reg, filepath.Join(dir, "nope.ttf")); err == nil {
		t.Error("粗体字体不存在时应报错")
	}
}

// stubFace 是最小的 font.Face 实现，只用来构造「缺字形且推进为 0」这种
// 真实 opentype 字面几乎不会出现的情况（opentype 缺字形时仍返回 .notdef
// 的推进），专门锁住 MeasureString 的兜底分支。
type stubFace struct{ ascent fixed.Int26_6 }

func (stubFace) Close() error { return nil }
func (stubFace) Glyph(fixed.Point26_6, rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	return image.Rectangle{}, nil, image.Point{}, 0, false
}
func (stubFace) GlyphBounds(rune) (fixed.Rectangle26_6, fixed.Int26_6, bool) {
	return fixed.Rectangle26_6{}, 0, false
}
func (stubFace) GlyphAdvance(r rune) (fixed.Int26_6, bool) {
	if r == 'W' {
		return fixed.I(10), true
	}
	return 0, false // 缺字形且没有 .notdef 推进
}
func (stubFace) Kern(rune, rune) fixed.Int26_6 { return 0 }
func (f stubFace) Metrics() font.Metrics       { return font.Metrics{Ascent: f.ascent} }

func TestMeasureStringFallbackWhenAdvanceIsZero(t *testing.T) {
	f := stubFace{ascent: fixed.I(20)} // 兜底应为 10px/字
	if got := MeasureString(f, "中文"); got != 20 {
		t.Errorf("缺字形且推进为 0 时应按 ascent/2 兜底，得到 %d，期望 20", got)
	}
	// 有字形的部分不能被兜底顶替
	if got := MeasureString(f, "W中"); got != 20 {
		t.Errorf("有字形 W(10) + 兜底(10) 应为 20，得到 %d", got)
	}
}

// negBearingFace 是一个只有负左边距字形的假字面，用来锁住 TextColumnLeft。
// STHeiti 的「2」实测左边距 −1px；gofont 全是正的，真字体不在 CI 上，
// 所以用合成的字面把这个行为钉死。
type negBearingFace struct {
	lsb fixed.Int26_6 // 负的左边距
}

func (negBearingFace) Close() error { return nil }
func (f negBearingFace) Glyph(pt fixed.Point26_6, r rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	if r != '2' {
		return image.Rectangle{}, nil, image.Point{}, fixed.I(10), false
	}
	// 4×4 黑块，左上角落在 pt + lsb 处（负边距 = 左越笔位）
	x := int((pt.X + f.lsb) >> 6)
	y := int(pt.Y>>6) - 8
	return image.Rect(x, y, x+4, y+4), image.NewUniform(color.Gray{Y: 0}),
		image.Point{}, fixed.I(10), true
}
func (f negBearingFace) GlyphBounds(r rune) (fixed.Rectangle26_6, fixed.Int26_6, bool) {
	if r != '2' {
		return fixed.Rectangle26_6{}, fixed.I(10), false
	}
	return fixed.Rectangle26_6{
		Min: fixed.Point26_6{X: f.lsb, Y: -fixed.I(8)},
		Max: fixed.Point26_6{X: f.lsb + fixed.I(4), Y: 0},
	}, fixed.I(10), true
}
func (negBearingFace) GlyphAdvance(r rune) (fixed.Int26_6, bool) { return fixed.I(10), true }
func (negBearingFace) Kern(rune, rune) fixed.Int26_6             { return 0 }
func (negBearingFace) Metrics() font.Metrics {
	return font.Metrics{Ascent: fixed.I(10), Descent: fixed.I(2)}
}

// 首字形有负左边距时，TextColumnLeft 必须把墨迹推到 x 之右，Text 则不会。
func TestTextColumnLeftShiftsNegativeBearingInside(t *testing.T) {
	f := negBearingFace{lsb: -fixed.I(1)}

	// 对照：Text 会把墨迹画到 x−1
	ref := white()
	Text(ref, f, "2", 50, 60, color.Gray{0})
	minXRef := -1
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			if ref.GrayAt(x, y).Y != 255 {
				if minXRef < 0 || x < minXRef {
					minXRef = x
				}
			}
		}
	}
	if minXRef >= 50 {
		t.Fatalf("对照组的墨迹左缘 %d 没有左越 x=50 —— 假字面没构造出负边距", minXRef)
	}

	img := white()
	TextColumnLeft(img, f, "2", 50, 60, color.Gray{0})
	minX := -1
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			if img.GrayAt(x, y).Y != 255 {
				if minX < 0 || x < minX {
					minX = x
				}
			}
		}
	}
	if minX < 50 {
		t.Errorf("TextColumnLeft 的墨迹左缘 = %d，应 ≥ x=50（不得左越列的左边界）", minX)
	}
}

// 左边距为正时 TextColumnLeft 不得移动笔位（否则右栏所有文本都会右移）。
func TestTextColumnLeftLeavesPositiveBearingAlone(t *testing.T) {
	f := negBearingFace{lsb: fixed.I(2)}
	a := white()
	Text(a, f, "2", 50, 60, color.Gray{0})
	b := white()
	TextColumnLeft(b, f, "2", 50, 60, color.Gray{0})
	if !imagesEqualGray(a, b) {
		t.Error("正左边距时 TextColumnLeft 不应改变绘制位置")
	}
}

func imagesEqualGray(a, b *image.Gray) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}

// 描边只能写环带，不能碰内部：先把内部填黑，描边（白色）之后中心必须仍是黑。
//
// 这条直接针对「填外圈再填内圈白块」的实现 —— 那种写法会把已经画好的
// 日号擦掉，而调用方一旦先画数字后描边，界面上的选中日期就没有日号。
func TestStrokeRoundRectLeavesInterior(t *testing.T) {
	img := white()
	const x, y, w, h, rad = 50, 50, 100, 60, 10
	FillRect(img, x, y, w, h, ColorBlack) // 模拟「已经有内容」

	strokeRoundRect(img, x, y, w, h, rad, 2, ColorWhite)

	// 内部没有被环带覆盖的像素仍是黑。
	for _, p := range []image.Point{
		{x + w/2, y + h/2},
		{x + rad + 2, y + rad + 2},
		{x + w - rad - 2, y + h - rad - 2},
	} {
		if got := img.GrayAt(p.X, p.Y).Y; got != 0 {
			t.Errorf("内部点 (%d,%d) 灰度 %d，期望 0（被擦掉了）", p.X, p.Y, got)
		}
	}
	// 环带确实画了：上边中点与左边中点应为白。
	for _, p := range []image.Point{{x + w/2, y}, {x, y + h/2}} {
		if got := img.GrayAt(p.X, p.Y).Y; got != 255 {
			t.Errorf("环带点 (%d,%d) 灰度 %d，期望 255（没画出来）", p.X, p.Y, got)
		}
	}
}
