package touch

import (
	"encoding/binary"
	"errors"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	evSyn = 0
	evKey = 1
	evAbs = 3

	btnTouch = 330

	absMTSlot       = 47
	absMTX          = 53
	absMTY          = 54
	absMTTrackingID = 57

	// eventSize 是 32 位 ARM 上 struct input_event 的大小：
	// timeval(2×int32) + type(u16) + code(u16) + value(s32)。
	eventSize = 16
)

// parser 把字节流变成手势事件。抽出来是为了测试能直接喂合成数据。
type parser struct {
	emit   func(Event)
	rotate string

	// 当前手势的起点（竖版坐标）。按下后第一个 SYN_REPORT 才锁定：
	// BTN_TOUCH 先于本帧坐标到达，那一刻 lastX/lastY 还是旧值（首次触摸是 0）。
	down     bool
	startSet bool
	startX   int
	startY   int
	lastX    int
	lastY    int

	// btnDown 是 BTN_TOUCH 的当前状态，和「是否在跟一根手指」分开记。
	// 第一根抬起后 BTN_TOUCH 仍可能为 1；此时新落下的手指不能当成新手势。
	btnDown bool

	// 协议 B 用 ABS_MT_SLOT 切换「当前作用的触点」。只跟按下时锁定的那个
	// slot，其它手指的坐标一律忽略。
	slot       int
	activeSlot int
	// slotLocked 表示 activeSlot 已经由本次手势的第一根手指锁定。
	// 不能在 BTN_TOUCH 时就锁：驱动切 slot 的报文可能还没到，p.slot
	// 可能还是上一次多指手势留下的残值。
	slotLocked bool

	// pending 保存上一次 write 里不足一个事件的残片。evdev 的 read(2)
	// 本应只返回完整事件，但保留缓冲可以避免异常驱动让整条流永久错位。
	pending []byte
}

func newParser(emit func(Event), rotate string) *parser {
	return &parser{emit: emit, rotate: rotate}
}

// write 解析一段字节流。跨 read 的残片会保留到下一次调用。
func (p *parser) write(b []byte) error {
	if len(p.pending) > 0 {
		merged := make([]byte, 0, len(p.pending)+len(b))
		merged = append(merged, p.pending...)
		merged = append(merged, b...)
		b = merged
		p.pending = nil
	}
	n := len(b) / eventSize * eventSize
	for off := 0; off < n; off += eventSize {
		typ := binary.LittleEndian.Uint16(b[off+8:])
		code := binary.LittleEndian.Uint16(b[off+10:])
		value := int32(binary.LittleEndian.Uint32(b[off+12:]))
		p.feed(typ, code, value)
	}
	if n < len(b) {
		p.pending = append(p.pending[:0], b[n:]...)
	}
	return nil
}

func (p *parser) feed(typ, code uint16, value int32) {
	switch {
	case typ == evKey && code == btnTouch:
		// 只有 0 才算释放；1 是按下，其它值（内核不会发）忽略。
		switch value {
		case 1:
			p.btnDown = true
			p.begin()
		case 0:
			p.btnDown = false
			p.finish()
		}
	case typ == evAbs && code == absMTSlot:
		p.slot = int(value)
	case typ == evAbs && code == absMTTrackingID:
		if value == -1 {
			// 只有活动 slot 抬起才算手势结束。
			if p.down && p.slotLocked && p.slot == p.activeSlot {
				p.finish()
			}
			return
		}
		if !p.down {
			if p.btnDown {
				// 幽灵：BTN_TOUCH 仍按着，但第一根已经抬起，新落下的
				// 手指不能开新手势。
				return
			}
			// 兼容先发 TRACKING_ID、后发 BTN_TOUCH 的设备。
			p.begin()
		}
		// 新手势由第一根真正落下的手指锁定 activeSlot。
		// 不能在 BTN_TOUCH 时用 p.slot 定 —— 那时驱动切 slot 的操作
		// 可能还没到，p.slot 可能是上一次多指手势留下的残值，会把
		// activeSlot 锁错，导致整次触摸的坐标全被丢弃，最后拿上一轮
		// 的坐标产出幽灵手势。
		if !p.slotLocked {
			p.activeSlot = p.slot
			p.slotLocked = true
		}
	case typ == evAbs && code == absMTX && p.down && p.slotLocked && p.slot == p.activeSlot:
		p.lastX = int(value)
	case typ == evAbs && code == absMTY && p.down && p.slotLocked && p.slot == p.activeSlot:
		p.lastY = int(value)
	case typ == evSyn && code == 0:
		// 本帧数据收齐。刚开始触摸还没锁起点，就锁在这里。
		if p.down && p.slotLocked && !p.startSet {
			p.startX, p.startY = p.lastX, p.lastY
			p.startSet = true
		}
	}
}

// begin 记下一次触摸已开始。activeSlot 不在这里锁，留给第一个
// 非 -1 的 TRACKING_ID（见 feed）。
func (p *parser) begin() {
	if p.down {
		return // 已在跟踪第一根手指，后续的按下是第二根手指，忽略
	}
	p.down = true
	p.startSet = false
	p.slotLocked = false
}

func (p *parser) finish() {
	if !p.down {
		return
	}
	p.down = false
	p.startSet = false
	// 不重置 p.slot：它是驱动当前 slot 状态的影子，重置会让
	// 「驱动省略冗余 ABS_MT_SLOT」时的 activeSlot 记错。
	p.activeSlot = 0
	p.slotLocked = false

	dx := p.lastX - p.startX
	dy := p.lastY - p.startY
	dist := abs(dx) + abs(dy)

	x, y := ToLandscape(p.lastX, p.lastY, p.rotate)
	// 起点也换成横版坐标：调用方要用落手点做命中判定，不能在别的包里
	// 再推一遍旋转公式。
	sx, sy := ToLandscape(p.startX, p.startY, p.rotate)

	switch {
	case dist <= tapMaxDist:
		p.emit(Event{Kind: KindTap, X: x, Y: y, StartX: sx, StartY: sy})
	case dist >= swipeMinDist:
		// 方向判定要在**横版坐标**里做：竖版主轴与横版主轴差 90°。
		ldx, ldy := x-sx, y-sy
		if abs(ldx) >= abs(ldy) {
			if ldx > 0 {
				p.emit(Event{Kind: KindSwipeRight, X: x, Y: y, StartX: sx, StartY: sy})
			} else {
				p.emit(Event{Kind: KindSwipeLeft, X: x, Y: y, StartX: sx, StartY: sy})
			}
		} else {
			if ldy > 0 {
				p.emit(Event{Kind: KindSwipeDown, X: x, Y: y, StartX: sx, StartY: sy})
			} else {
				p.emit(Event{Kind: KindSwipeUp, X: x, Y: y, StartX: sx, StartY: sy})
			}
		}
	}
	// 死区内不产出事件
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Reader 持续读触摸设备并把识别出的手势送到 channel。
type Reader struct {
	f    *os.File
	ch   chan Event
	done chan struct{}

	once sync.Once
	err  error
}

// Open 打开触摸设备。失败时调用方应禁用交互而不是退出 —— 时钟要照常走。
//
// 以非阻塞方式打开，并且必须走 os.File.Read（不要用裸 syscall.Read）：
// 文件会注册进 runtime netpoll，Read 阻塞时 goroutine 让出，Close 能把它
// 唤醒。只 close(fd) 打不断裸 syscall.Read 在内核里的阻塞。
func Open(path, rotate string) (*Reader, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	r := &Reader{f: f, ch: make(chan Event, 8), done: make(chan struct{})}
	p := newParser(func(e Event) {
		select {
		case <-r.done:
			return // 已关闭，别再投递迟到事件
		default:
		}
		select {
		case r.ch <- e:
		case <-r.done:
		}
	}, rotate)
	go r.loop(p)
	return r, nil
}

// Events 返回手势 channel。Reader 关闭后该 channel 会被关闭。
func (r *Reader) Events() <-chan Event { return r.ch }

func (r *Reader) loop(p *parser) {
	defer close(r.ch)
	buf := make([]byte, eventSize*64)
	for {
		n, err := r.f.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}
		if n == 0 {
			// 理论上 os.File 会把 0 读转成 io.EOF；真遇到 0 时避免忙等。
			time.Sleep(time.Millisecond)
			continue
		}
		if err := p.write(buf[:n]); err != nil {
			return
		}
	}
}

// Close 关闭设备并结束读取协程。可重复调用，之后的调用返回第一次的错误。
func (r *Reader) Close() error {
	r.once.Do(func() {
		close(r.done)
		r.err = r.f.Close()
	})
	return r.err
}
