package display

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rhsy/kindleclock/src/internal/render"
)

// blankPortrait 返回全白（255）的竖版图。
// 不能用 image.NewGray 的零值：那是全黑（0），而下面的用例靠
// 「把某点涂黑」制造差异 —— 全黑背景上涂黑等于没变，测试会假绿。
func blankPortrait() *image.Gray {
	img := image.NewGray(image.Rect(0, 0, render.CanvasH, render.CanvasW))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

// ---- 脏区域 ----

func TestDifferFirstFrameAllDirty(t *testing.T) {
	d := NewDiffer()
	img := blankPortrait()
	regions, _ := MapRegions(render.Regions(), "cw")

	dirty := d.Dirty(img, regions)
	if len(dirty) != len(regions) {
		t.Fatalf("首帧应全部为脏，得到 %d/%d", len(dirty), len(regions))
	}
}

func TestDifferNoChangeNoDirty(t *testing.T) {
	d := NewDiffer()
	img := blankPortrait()
	regions, _ := MapRegions(render.Regions(), "cw")

	d.Dirty(img, regions) // 首帧
	if dirty := d.Dirty(img, regions); len(dirty) != 0 {
		t.Errorf("内容未变时不应有脏区域，得到 %d 个", len(dirty))
	}
}

func TestDifferDetectsSingleRegion(t *testing.T) {
	d := NewDiffer()
	regions, _ := MapRegions(render.Regions(), "cw")

	a := blankPortrait()
	d.Dirty(a, regions)

	b := blankPortrait()
	// 只改 clock 区中心一个像素
	for _, r := range regions {
		if r.ID == "clock" {
			b.SetGray(r.Rect.X+10, r.Rect.Y+10, color.Gray{Y: 0})
		}
	}

	dirty := d.Dirty(b, regions)
	if len(dirty) != 1 || dirty[0].ID != "clock" {
		t.Fatalf("应只有 clock 变脏，得到 %v", ids(dirty))
	}
}

func TestDifferDetectsMultipleRegions(t *testing.T) {
	d := NewDiffer()
	regions, _ := MapRegions(render.Regions(), "cw")

	d.Dirty(blankPortrait(), regions)

	b := blankPortrait()
	for _, r := range regions {
		if r.ID == "clock" || r.ID == "courses" {
			b.SetGray(r.Rect.X+5, r.Rect.Y+5, color.Gray{Y: 0})
		}
	}
	dirty := d.Dirty(b, regions)
	if len(dirty) != 2 {
		t.Fatalf("应有两个区域变脏，得到 %v", ids(dirty))
	}
}

func TestDifferDoesNotMutateInput(t *testing.T) {
	d := NewDiffer()
	regions, _ := MapRegions(render.Regions(), "cw")

	// 全部元素快照（原版只比 regions[0]，覆盖不到原地过滤的写法）
	before := make([]render.Region, len(regions))
	copy(before, regions)

	dirty := d.Dirty(blankPortrait(), regions)

	for i := range before {
		if regions[i] != before[i] {
			t.Errorf("Dirty 改写了入参第 %d 项", i)
		}
	}

	// 返回值不能复用入参底层数组。
	// 危险写法：`out := regions[:0]` 然后 append —— 返回值别名入参，
	// 调用方后续 append 会改写别人的切片。
	if len(dirty) > 0 && &dirty[0] == &regions[0] {
		t.Fatal("Dirty 复用了入参的底层数组")
	}
}

func TestDifferResetForcesAllDirty(t *testing.T) {
	d := NewDiffer()
	regions, _ := MapRegions(render.Regions(), "cw")
	d.Dirty(blankPortrait(), regions)
	d.Reset()
	if dirty := d.Dirty(blankPortrait(), regions); len(dirty) != len(regions) {
		t.Errorf("Reset 后应全部变脏，得到 %d", len(dirty))
	}
}

func ids(rs []render.Region) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

// ---- PNG 切片写出 ----

func TestWriteRegionPNG(t *testing.T) {
	dir := t.TempDir()
	img := image.NewGray(image.Rect(0, 0, render.CanvasH, render.CanvasW))
	// 全图按坐标填值：这样「从 (0,0) 裁」和「从正确位置裁」结果不同
	for y := 0; y < render.CanvasW; y++ {
		for x := 0; x < render.CanvasH; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((x*7 + y*13) % 256)})
		}
	}
	regions, _ := MapRegions(render.Regions(), "cw")
	r := regions[0].Rect

	p := filepath.Join(dir, "r.png")
	if err := WriteRegionPNG(img, r, p); err != nil {
		t.Fatal(err)
	}

	f, _ := os.Open(p)
	defer f.Close()
	dec, err := decodePNG(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := dec.Bounds(); b.Dx() != r.W || b.Dy() != r.H {
		t.Fatalf("切片尺寸 = %dx%d，期望 %dx%d", b.Dx(), b.Dy(), r.W, r.H)
	}

	// 四个角的值必须与源图的对应位置一致 —— 锁住裁剪原点
	corners := [][4]int{
		{0, 0, r.X, r.Y},
		{r.W - 1, 0, r.X + r.W - 1, r.Y},
		{0, r.H - 1, r.X, r.Y + r.H - 1},
		{r.W - 1, r.H - 1, r.X + r.W - 1, r.Y + r.H - 1},
	}
	for _, c := range corners {
		want := img.GrayAt(c[2], c[3]).Y
		got := dec.GrayAt(c[0], c[1]).Y
		if got != want {
			t.Errorf("子图 (%d,%d) = %d，源图 (%d,%d) = %d —— 裁剪原点错了",
				c[0], c[1], got, c[2], c[3], want)
		}
	}
}

// 与图像无交集的区域应静默失败还是报错？必须报错 ——
// 否则调用方会拿到一张 0×0 的 PNG 交给 fbink，错误被推后且难查。
func TestWriteRegionPNGRejectsEmptyRect(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 20, 20))
	err := WriteRegionPNG(img, render.Rect{X: 100, Y: 100, W: 10, H: 10},
		filepath.Join(t.TempDir(), "r.png"))
	if err == nil {
		t.Fatal("越界区域应报错")
	}
}

func TestWriteRegionPNGCreateError(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 20, 20))
	err := WriteRegionPNG(img, render.Rect{X: 0, Y: 0, W: 10, H: 10},
		filepath.Join(t.TempDir(), "no-such-dir", "r.png"))
	if err == nil {
		t.Fatal("目录不存在时应报错")
	}
}

// ---- fbink 调用 ----

// fakeFbink 生成一个会记录参数的假 fbink 脚本
func fakeFbink(t *testing.T) (path, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> '" + logPath + "'\nprintf '\\n' >> '" + logPath + "'\nexit 0\n"
	path = filepath.Join(dir, "fake-fbink")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, logPath
}

func TestFbinkDrawRegionArgs(t *testing.T) {
	bin, log := fakeFbink(t)
	f := NewFbink(bin)

	r := render.Rect{X: 10, Y: 20, W: 100, H: 50}
	if err := f.DrawRegion(context.Background(), "/tmp/x.png", r, "REAGL"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(log)
	got := string(data)
	for _, want := range []string{
		"-q", "-g", "file=/tmp/x.png,x=10,y=20", "-W", "REAGL", "-w",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("调用参数缺少 %q\n实际:\n%s", want, got)
		}
	}
}

func TestFbinkClearFullArgs(t *testing.T) {
	bin, log := fakeFbink(t)
	f := NewFbink(bin)
	if err := f.ClearFull(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "-k") {
		t.Errorf("清屏应使用 -k，实际:\n%s", data)
	}
}

func TestFbinkReportsError(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fail-fbink")
	os.WriteFile(bin, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755)

	f := NewFbink(bin)
	err := f.DrawRegion(context.Background(), "/tmp/x.png", render.Rect{W: 10, H: 10}, "GC16")
	if err == nil {
		t.Fatal("非零退出码应报错")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("错误应包含 fbink 的 stderr，得到 %v", err)
	}
}

func TestFbinkMissingBinary(t *testing.T) {
	f := NewFbink("/nonexistent/fbink-xyz")
	err := f.DrawRegion(context.Background(), "/tmp/x.png", render.Rect{W: 10, H: 10}, "GC16")
	if err == nil {
		t.Fatal("二进制不存在应报错")
	}
}

func TestFbinkDrawFullArgs(t *testing.T) {
	bin, log := fakeFbink(t)
	f := NewFbink(bin)
	if err := f.DrawFull(context.Background(), "/tmp/full.png"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	got := string(data)
	// -c 清屏、file= 全幅、GC16 带闪烁波形、-w 等完成
	for _, want := range []string{"-c", "file=/tmp/full.png", "-W", "GC16", "-w"} {
		if !strings.Contains(got, want) {
			t.Errorf("DrawFull 缺少参数 %q\n实际:\n%s", want, got)
		}
	}
}

// 设置页开关用无清屏的全屏绘制：图像本身盖满整屏，-c 是多余的第二次
// 面板更新（实测 0.5s -> 0.15s）。
func TestFbinkDrawFullNoClearArgs(t *testing.T) {
	bin, log := fakeFbink(t)
	f := NewFbink(bin)
	if err := f.DrawFullNoClear(context.Background(), "/tmp/full.png"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	got := string(data)
	for _, want := range []string{"-g", "file=/tmp/full.png", "-W", "GC16", "-w"} {
		if !strings.Contains(got, want) {
			t.Errorf("DrawFullNoClear 缺少参数 %q\n实际:\n%s", want, got)
		}
	}
	if strings.Contains(got, " -c ") || strings.HasPrefix(strings.TrimSpace(got), "-q -c") {
		t.Errorf("DrawFullNoClear 不该带 -c（清屏），实际:\n%s", got)
	}
	if strings.Contains(got, " -k ") {
		t.Errorf("DrawFullNoClear 不该带 -k，实际:\n%s", got)
	}
}

// 调用方没给 deadline 时必须自己补超时 —— 否则 fbink 卡住会让时钟停摆。
//
// 这里把同一段逻辑的超时缩到 300ms，让「补超时」真正被观察到：
// 卡住的脚本会被强杀、调用返回错误。只测「慢命令能跑完」的话，
// 把补超时的代码整段删掉测试依然全绿，等于没锁。
func TestFbinkAppliesDefaultTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hang-fbink")
	os.WriteFile(bin, []byte("#!/bin/sh\nsleep 5\nexit 0\n"), 0o755)

	f := NewFbink(bin)
	if f.timeout != callTimeout {
		t.Fatalf("新 Fbink 的默认超时应为 %v，实际 %v", callTimeout, f.timeout)
	}
	f.timeout = 300 * time.Millisecond

	start := time.Now()
	err := f.DrawRegion(context.Background(), "/tmp/x.png", render.Rect{W: 10, H: 10}, "GC16")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("卡住的 fbink 应被超时打断并报错")
	}
	if elapsed > 2*time.Second {
		t.Errorf("耗时 %v，说明没有补上默认超时", elapsed)
	}
}

// 调用方带了 deadline 时必须真的把它传到子进程上并被强杀。
//
// 这条锁住 exec.CommandContext 的 ctx 传播：若误写成 exec.Command，
// 卡死的 fbink 会无视调用方的超时，一直挂在那里。
func TestFbinkHonorsCtxDeadline(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hang-fbink")
	os.WriteFile(bin, []byte("#!/bin/sh\nsleep 5\nexit 0\n"), 0o755)

	f := NewFbink(bin)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := f.DrawRegion(ctx, "/tmp/x.png", render.Rect{W: 10, H: 10}, "GC16")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("ctx 超时后应报错")
	}
	if elapsed > 2*time.Second {
		t.Errorf("耗时 %v，ctx 的 deadline 没有传到子进程", elapsed)
	}
}

// decodePNG 返回 *image.Gray：测试要用 GrayAt 逐点比对，
// 返回 image.Image 接口就没有这个方法了。
func decodePNG(r io.Reader) (*image.Gray, error) {
	img, err := png.Decode(r)
	if err != nil {
		return nil, err
	}
	g, ok := img.(*image.Gray)
	if !ok {
		return nil, fmt.Errorf("解码结果不是灰度图，而是 %T", img)
	}
	return g, nil
}

// 造一个假的 framebuffer + sysfs，用于测 ReadVisibleFrame。
func fakeFB(t *testing.T, stride, bpp int, fill func(*image.Gray)) (fbPath, sysDir string) {
	t.Helper()
	dir := t.TempDir()
	sysDir = filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "stride"),
		[]byte(strconv.Itoa(stride)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "bits_per_pixel"),
		[]byte(strconv.Itoa(bpp)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	img := image.NewGray(image.Rect(0, 0, portraitW, portraitH))
	fill(img)

	// framebuffer 按 stride 排布：每行前 portraitW 个字节是像素，
	// 其余是硬件填充（实测 PW3 是 16 列）。
	buf := make([]byte, stride*portraitH)
	for y := 0; y < portraitH; y++ {
		copy(buf[y*stride:y*stride+portraitW], img.Pix[y*portraitW:(y+1)*portraitW])
		// 填充区写成 0xAA，验证读回时被裁掉
		for x := portraitW; x < stride; x++ {
			buf[y*stride+x] = 0xAA
		}
	}
	fbPath = filepath.Join(dir, "fb0")
	if err := os.WriteFile(fbPath, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return fbPath, sysDir
}

func TestReadVisibleFrameCropsStride(t *testing.T) {
	want := image.NewGray(image.Rect(0, 0, portraitW, portraitH))
	for i := range want.Pix {
		want.Pix[i] = uint8(i % 251)
	}
	fbPath, sysDir := fakeFB(t, 1088, 8, func(g *image.Gray) {
		copy(g.Pix, want.Pix)
	})

	got, err := ReadVisibleFrame(fbPath, sysDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds().Dx() != portraitW || got.Bounds().Dy() != portraitH {
		t.Fatalf("尺寸不对: %v", got.Bounds())
	}
	for i := range want.Pix {
		if got.Pix[i] != want.Pix[i] {
			t.Fatalf("第 %d 个像素不对: got %d want %d（填充区没被裁掉？）",
				i, got.Pix[i], want.Pix[i])
		}
	}
}

// 位深不是 8 时必须报错，而不是悄悄按 8 位解释（那会得到一张乱码图）。
func TestReadVisibleFrameRejectsNon8bpp(t *testing.T) {
	fbPath, sysDir := fakeFB(t, 1088, 16, func(*image.Gray) {})
	if _, err := ReadVisibleFrame(fbPath, sysDir); err == nil {
		t.Fatal("16bpp 应该报错")
	}
}

func TestReadVisibleFrameMissingSysfs(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadVisibleFrame(filepath.Join(dir, "nope"),
		filepath.Join(dir, "nosys")); err == nil {
		t.Fatal("缺 sysfs 应该报错")
	}
}

func TestInkMismatchRatio(t *testing.T) {
	blank := image.NewGray(image.Rect(0, 0, portraitW, portraitH))
	for i := range blank.Pix {
		blank.Pix[i] = 0xFF
	}

	// 「我们画的」内容：左上角一块黑，中间一条浅灰横线
	want := image.NewGray(blank.Rect)
	copy(want.Pix, blank.Pix)
	for y := 100; y < 300; y++ {
		for x := 100; x < 400; x++ {
			want.Pix[y*portraitW+x] = 0
		}
	}
	for x := 0; x < portraitW; x++ {
		want.Pix[700*portraitW+x] = 179 // ColorLight
	}

	// 1. 完全一致 -> 0
	if r := InkMismatchRatio(want, want, 4); r != 0 {
		t.Fatalf("同一张图应为 0，得到 %v", r)
	}

	// 2. 整屏变白（别人把我们的画面全盖了）-> 接近 1
	if r := InkMismatchRatio(blank, want, 4); r < 0.98 {
		t.Fatalf("整屏被盖应为 1 左右，得到 %v", r)
	}

	// 3. 只有状态栏压在右侧一条（真机上的正常情况）-> 仍接近 0
	status := image.NewGray(blank.Rect)
	copy(status.Pix, want.Pix)
	for y := 0; y < portraitH; y++ {
		for x := portraitW - 7; x < portraitW; x++ {
			status.Pix[y*portraitW+x] = 0
		}
	}
	if r := InkMismatchRatio(status, want, 4); r > 0.05 {
		t.Fatalf("只压住右边缘 7 列不该算被盖，得到 %v", r)
	}

	// 4. 轻微残影（灰度差 10）不算
	ghost := image.NewGray(blank.Rect)
	copy(ghost.Pix, want.Pix)
	for i := range ghost.Pix {
		if ghost.Pix[i] < 200 {
			ghost.Pix[i] += 10
		}
	}
	if r := InkMismatchRatio(ghost, want, 4); r != 0 {
		t.Fatalf("10 级残影不该算差异，得到 %v", r)
	}

	// 5. 尺寸不一致 -> 1
	if r := InkMismatchRatio(image.NewGray(image.Rect(0, 0, 10, 10)), want, 4); r != 1 {
		t.Fatalf("尺寸不一致应返回 1，得到 %v", r)
	}

	// 6. 我们什么都没画（全白）-> 样本不足，返回 0，不下结论
	if r := InkMismatchRatio(blank, blank, 4); r != 0 {
		t.Fatalf("没有笔画可判时应返回 0，得到 %v", r)
	}
}

// 全屏刷新必须带 -f（强制黑闪）。
//
// 本地实测：不加 -f 的 GC16 清不掉从 Kindle 书库切回台历时留下的残影 ——
// 连点几次「立即全屏刷新」都没用。加 -f 才是书页翻动那种彻底刷新。
func TestFullScreenRefreshesForceFlash(t *testing.T) {
	bin, log := fakeFbink(t)
	f := NewFbink(bin)
	ctx := context.Background()

	if err := f.ClearFull(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.DrawFull(ctx, "/tmp/x.png"); err != nil {
		t.Fatal(err)
	}
	if err := f.DrawFullNoClear(ctx, "/tmp/x.png"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	// 三次调用各占一段，逐段检查
	for i, seg := range strings.Split(strings.TrimSpace(string(data)), "\n\n") {
		if !strings.Contains(seg, "-f") {
			t.Fatalf("第 %d 次全屏刷新缺少 -f（强制黑闪）：\n%s", i+1, seg)
		}
	}
}
