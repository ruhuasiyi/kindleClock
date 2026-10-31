package display

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/rhsy/kindleclock/src/internal/render"
)

// callTimeout 是单次 fbink 调用的上限（spec §5.4）。
//
// 超时视为失败并记日志：宁可这一帧没刷上，也不能让时钟永久停摆。
const callTimeout = 10 * time.Second

// Differ 通过区域哈希找出内容变化的区域。
//
// 用 FNV-1a 对灰度像素做哈希：快、内存恒定。相比逐像素比对，哈希
// 能在恒定内存下判断"整块是否相同"。代价是理论上的碰撞 —— 64 位
// 下概率可忽略，真碰上时后果是**漏刷**（内容变了但指纹相同，判为
// 不脏），不会多刷。
type Differ struct {
	mu   sync.Mutex
	last map[string]uint64
}

func NewDiffer() *Differ {
	return &Differ{last: make(map[string]uint64)}
}

// Reset 清空历史，使下一帧全部视为脏区（用于强制全屏重绘）。
func (d *Differ) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last = make(map[string]uint64)
}

// Dirty 返回内容有变化的区域，并记录本帧指纹。
//
// 返回的是新切片，不修改入参。
func (d *Differ) Dirty(img *image.Gray, regions []render.Region) []render.Region {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]render.Region, 0, len(regions))
	for _, r := range regions {
		h := hashRegion(img, r.Rect)
		prev, seen := d.last[r.ID]
		if !seen || prev != h {
			out = append(out, r)
		}
		d.last[r.ID] = h
	}
	return out
}

// hashRegion 只对区域内的像素做 FNV-1a，避免读整幅图。
//
// 用 PixOffset 而非 y*Stride+x：后者隐含「图像原点为 (0,0)」，
// 传入 SubImage 产物（Rect().Min 非零）会读错行，甚至越界 panic。
//
// 逐行算一次偏移、整行一次 Write：比逐像素调用快约 2 倍。
func hashRegion(img *image.Gray, r render.Rect) uint64 {
	h := fnv.New64a()
	rect := r.Standard().Intersect(img.Bounds())
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		base := img.PixOffset(rect.Min.X, y)
		h.Write(img.Pix[base : base+rect.Dx()])
	}
	return h.Sum64()
}

// WriteRegionPNG 把竖版图的一块矩形写成 PNG。
//
// 单独成文件而不是拼进一张大图：fbink 只接受文件路径，无法从内存
// 读图；而每帧都写整幅 1072×1448 的 PNG 又太慢。
//
// 直接调具体方法而不是走接口断言：没有必要 —— 具体类型更清晰，
// 也免得后来人以为这里有什么类型陷阱。
func WriteRegionPNG(img *image.Gray, r render.Rect, path string) error {
	rect := r.Standard().Intersect(img.Bounds())
	if rect.Dx() <= 0 || rect.Dy() <= 0 {
		return fmt.Errorf("区域 %v 与图像 %v 无交集", r, img.Bounds())
	}
	sub := img.SubImage(rect).(*image.Gray)

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建 %s: %w", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, sub); err != nil {
		return fmt.Errorf("编码 %s: %w", path, err)
	}
	return nil
}

// Fbink 封装对 fbink 可执行文件的调用。
type Fbink struct {
	path string
	// timeout 是单次调用的上限，NewFbink 时置为 callTimeout。
	// 单列为字段只为让测试用短超时验证同一段逻辑（等 10 秒太慢）。
	timeout time.Duration
}

func NewFbink(path string) *Fbink {
	return &Fbink{path: path, timeout: callTimeout}
}

// DrawRegion 在指定坐标绘制一张 PNG，并只刷新该矩形。
//
// 参数：
//   - pngPath: 已写好的 PNG
//   - r:       竖版坐标下的矩形
//   - waveform: REAGL / GC16 / GL16 / DU 等
func (f *Fbink) DrawRegion(ctx context.Context, pngPath string, r render.Rect, waveform string) error {
	img := "file=" + pngPath +
		",x=" + strconv.Itoa(r.X) +
		",y=" + strconv.Itoa(r.Y)

	// -w 等待本次刷新真正落到屏幕再返回。
	// 不加的话连续多区域的更新会在 EPDC 队列里互相覆盖/合并，
	// 实测同一区域两次快速更新会丢掉前一次。代价是每次多等
	// 一个波形周期（实测 REAGL 约 13ms、GC16 约 16ms），可接受。
	args := []string{"-q", "-g", img, "-w"}
	if waveform != "" {
		args = append(args, "-W", waveform)
	}
	return f.run(ctx, args...)
}

// ClearFull 清屏（不显示任何内容）。
// forceFlash 让 e-ink 控制器在刷新时做一次**黑闪**。
//
// 为什么需要它：本地刷新的 GC16 在长期运行后不足以清掉面板上积累的残影 ——
// 真机上从 Kindle 书库切回台历时，书库的痕迹会淡淡地留在背景里，连点几次
// 「立即全屏刷新」也去不掉。加 -f 强制黑闪，才是书页翻动那种彻底的刷新。
const forceFlash = "-f"

func (f *Fbink) ClearFull(ctx context.Context) error {
	return f.run(ctx, "-q", "-k", "-W", "GC16", forceFlash, "-w")
}

// DrawFull 清屏后绘制一张全屏 PNG。
//
// 用于去残影：全屏 GC16 带完整波形，能把局部刷新累积的鬼影抹掉。
func (f *Fbink) DrawFull(ctx context.Context, pngPath string) error {
	// 全屏刷新尤其要等：它带闪烁波形，未完成时再画局部会花屏
	return f.run(ctx, "-q", "-c", "-g", "file="+pngPath, "-W", "GC16", forceFlash, "-w")
}

// DrawFullNoClear 绘制一张盖满整屏的 PNG，但不先清屏。
//
// 图像本身已经覆盖整屏，-c 会先把屏幕清一遍再画，对整屏图像等于两次
// 面板更新 —— 实测耗时从 ~0.15s 涨到 ~0.5s。设置页开关这种整屏切换
// 用这一版；局部刷新累积的残影仍由周期性的 DrawFull（带 -c）负责。
func (f *Fbink) DrawFullNoClear(ctx context.Context, pngPath string) error {
	return f.run(ctx, "-q", "-g", "file="+pngPath, "-W", "GC16", forceFlash, "-w")
}

// run 执行一次 fbink 调用。
//
// spec §5.4：每次调用超时 10 秒，超时视为失败。调用方传的 ctx 往往
// 是 signal.NotifyContext 的长生命周期 ctx（没有 deadline），
// 所以这里在缺失 deadline 时自己补一个 —— 否则 fbink 卡住会让时钟永久停摆。
func (f *Fbink) run(ctx context.Context, args ...string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, f.path, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// 没有 WaitDelay 时，进程被 ctx 杀掉后 Wait 仍会一直等 stderr
	// 管道关闭 —— 管道写端若被它的子进程继承，10 秒上限就形同虚设。
	// fbink 本身是单进程，正常路径不会触发；这是把超时真正变成硬上限。
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if msg == "" {
			return fmt.Errorf("fbink 失败: %w", err)
		}
		return fmt.Errorf("fbink 失败: %w: %s", err, msg)
	}
	return nil
}
