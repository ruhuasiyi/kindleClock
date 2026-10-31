package touch

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// ev 构造一条 16 字节的 input_event。
func ev(t int64, typ, code uint16, value int32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:], uint32(t))
	binary.LittleEndian.PutUint32(b[4:], 0)
	binary.LittleEndian.PutUint16(b[8:], typ)
	binary.LittleEndian.PutUint16(b[10:], code)
	binary.LittleEndian.PutUint32(b[12:], uint32(value))
	return b
}

// 一个完整的点击序列：按下 -> 坐标 -> SYN -> 抬起。
func tapStream(x, y int32) []byte {
	var b []byte
	b = append(b, ev(0, evKey, btnTouch, 1)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 0)...)
	b = append(b, ev(0, evAbs, absMTX, x)...)
	b = append(b, ev(0, evAbs, absMTY, y)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, -1)...)
	b = append(b, ev(0, evKey, btnTouch, 0)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	return b
}

// 一个滑动序列：按下 -> 若干中间点 -> 抬起。
func swipeStream(x0, y0, x1, y1 int32, steps int) []byte {
	var b []byte
	b = append(b, ev(0, evKey, btnTouch, 1)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 0)...)
	b = append(b, ev(0, evAbs, absMTX, x0)...)
	b = append(b, ev(0, evAbs, absMTY, y0)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	for i := 1; i <= steps; i++ {
		x := x0 + (x1-x0)*int32(i)/int32(steps)
		y := y0 + (y1-y0)*int32(i)/int32(steps)
		b = append(b, ev(0, evAbs, absMTX, x)...)
		b = append(b, ev(0, evAbs, absMTY, y)...)
		b = append(b, ev(0, evSyn, 0, 0)...)
	}
	b = append(b, ev(0, evAbs, absMTTrackingID, -1)...)
	b = append(b, ev(0, evKey, btnTouch, 0)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	return b
}

func feed(t *testing.T, stream []byte) []Event {
	t.Helper()
	var got []Event
	r := newParser(func(e Event) { got = append(got, e) }, "ccw")
	if err := r.write(stream); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestTapProducesOneTapEvent(t *testing.T) {
	// 竖版 (700, 600) 在 ccw 下应映射成横版 (1447-600, 700) = (847, 700)
	got := feed(t, tapStream(700, 600))
	if len(got) != 1 {
		t.Fatalf("一次点击应产出 1 个事件，得到 %d: %+v", len(got), got)
	}
	if got[0].Kind != KindTap {
		t.Fatalf("应是 Tap，得到 %v", got[0].Kind)
	}
	if got[0].X != 847 || got[0].Y != 700 {
		t.Fatalf("坐标映射错：得到 (%d,%d)，期望 (847,700)", got[0].X, got[0].Y)
	}
}

// 每个事件都要带上横版坐标的落手点。
//
// 主循环用落手点做命中：从课程区向上滑时，抬指点会落进日历网格，
// 用抬指点判定会误选日期。
func TestEventIncludesStartPoint(t *testing.T) {
	// 点击：竖版 (700,600) -> 横版 (847,700)，起点与终点相同。
	got := feed(t, tapStream(700, 600))
	if len(got) != 1 {
		t.Fatalf("一次点击应产出 1 个事件，得到 %d", len(got))
	}
	if got[0].StartX != 847 || got[0].StartY != 700 {
		t.Fatalf("Tap 起点映射错：得到 (%d,%d)，期望 (847,700)",
			got[0].StartX, got[0].StartY)
	}

	// 滑动：竖版起点 (500,700) -> 横版 (747,500)；终点 (540,700) -> (747,540)。
	got = feed(t, swipeStream(500, 700, 540, 700, 2))
	if len(got) != 1 {
		t.Fatalf("一次滑动应产出 1 个事件，得到 %d", len(got))
	}
	if got[0].StartX != 747 || got[0].StartY != 500 {
		t.Fatalf("Swipe 起点映射错：得到 (%d,%d)，期望 (747,500)",
			got[0].StartX, got[0].StartY)
	}
	if got[0].X != 747 || got[0].Y != 540 {
		t.Fatalf("Swipe 终点映射错：得到 (%d,%d)，期望 (747,540)",
			got[0].X, got[0].Y)
	}
}

func TestSwipeDirections(t *testing.T) {
	// 在竖版坐标里构造四个方向的位移，验证映射到横版后的方向判定。
	// ccw 下：竖版 +x -> 横版 +y；竖版 -y -> 横版 +x。
	cases := []struct {
		name   string
		dx, dy int32 // 竖版位移
		want   Kind
	}{
		{"竖版 +x 200", 200, 0, KindSwipeDown},
		{"竖版 -x 200", -200, 0, KindSwipeUp},
		{"竖版 -y 200", 0, -200, KindSwipeRight},
		{"竖版 +y 200", 0, 200, KindSwipeLeft},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := feed(t, swipeStream(500, 700, 500+c.dx, 700+c.dy, 5))
			if len(got) != 1 {
				t.Fatalf("一次滑动应产出 1 个事件，得到 %d", len(got))
			}
			if got[0].Kind != c.want {
				t.Fatalf("方向判定错：得到 %v，期望 %v", got[0].Kind, c.want)
			}
		})
	}
}

// 位移刚好在阈值两侧。
func TestGestureThresholds(t *testing.T) {
	// 小于 12px 且时间短 -> Tap（这里实际是 8px）
	if got := feed(t, swipeStream(500, 700, 508, 700, 2)); len(got) != 1 || got[0].Kind != KindTap {
		t.Fatalf("8px 位移应当是 Tap，得到 %+v", got)
	}
	// 40px 以上才算 Swipe；20px 既不是 Tap 也不是 Swipe -> 不产出事件
	if got := feed(t, swipeStream(500, 700, 520, 700, 2)); len(got) != 0 {
		t.Fatalf("20px 位移不该产出事件（既非 Tap 也非 Swipe），得到 %+v", got)
	}
	// 恰好 40px 应算 Swipe（阈值是 >=）。
	if got := feed(t, swipeStream(500, 700, 540, 700, 2)); len(got) != 1 || got[0].Kind != KindSwipeDown {
		t.Fatalf("恰好 40px 位移应当是 SwipeDown，得到 %+v", got)
	}
}

// 多指：只跟第一根手指。真机协议 B 每根手指各自发 ABS_MT_SLOT。
func TestMultiTouchUsesFirstFinger(t *testing.T) {
	var b []byte
	b = append(b, ev(0, evKey, btnTouch, 1)...)
	// 第一根手指：slot 0
	b = append(b, ev(0, evAbs, absMTSlot, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 5)...)
	b = append(b, ev(0, evAbs, absMTX, 300)...)
	b = append(b, ev(0, evAbs, absMTY, 400)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	// 第二根手指：slot 1
	b = append(b, ev(0, evAbs, absMTSlot, 1)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 6)...)
	b = append(b, ev(0, evAbs, absMTX, 900)...)
	b = append(b, ev(0, evAbs, absMTY, 1200)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	// 抬起第一根
	b = append(b, ev(0, evAbs, absMTSlot, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, -1)...)
	// 抬起第二根
	b = append(b, ev(0, evAbs, absMTSlot, 1)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, -1)...)
	b = append(b, ev(0, evKey, btnTouch, 0)...)
	b = append(b, ev(0, evSyn, 0, 0)...)

	got := feed(t, b)
	if len(got) != 1 {
		t.Fatalf("应产出 1 个事件，得到 %d: %+v", len(got), got)
	}
	// 位置应来自第一根手指 (300,400) -> 横版 (1047, 300)
	if got[0].X != 1047 || got[0].Y != 300 {
		t.Fatalf("应使用第一根手指的坐标，得到 (%d,%d)", got[0].X, got[0].Y)
	}
}

// 第一根抬起后 BTN_TOUCH 仍为 1，新落下的手指不能开新手势。
func TestSecondFingerAfterFirstLiftIsNotANewGesture(t *testing.T) {
	var b []byte
	b = append(b, ev(0, evKey, btnTouch, 1)...)
	b = append(b, ev(0, evAbs, absMTSlot, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 5)...)
	b = append(b, ev(0, evAbs, absMTX, 300)...)
	b = append(b, ev(0, evAbs, absMTY, 400)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	// 第一根抬起，但 BTN_TOUCH 仍为 1（另一根还按着）
	b = append(b, ev(0, evAbs, absMTSlot, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, -1)...)
	// 新手指落在 slot 1
	b = append(b, ev(0, evAbs, absMTSlot, 1)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 7)...)
	b = append(b, ev(0, evAbs, absMTX, 900)...)
	b = append(b, ev(0, evAbs, absMTY, 1200)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	b = append(b, ev(0, evKey, btnTouch, 0)...)
	b = append(b, ev(0, evSyn, 0, 0)...)

	got := feed(t, b)
	if len(got) != 1 {
		t.Fatalf("第二根手指不应开新手势，得到 %d: %+v", len(got), got)
	}
	if got[0].Kind != KindTap || got[0].X != 1047 || got[0].Y != 300 {
		t.Fatalf("应只产出第一根手指的 Tap (1047,300)，得到 %+v", got)
	}
}

// 双指手势之后紧接着一次单指点击，必须用第二次的坐标。
//
// 这条守的是一个真实存在的陷阱：finish() 有意不重置 p.slot（那是驱动状态的影子），
// 如果 begin() 在 BTN_TOUCH 时就用 p.slot 锁定 activeSlot，就会锁到上一轮的残值，
// 整次触摸的坐标全被丢弃，最后拿上一轮的坐标产出一个幽灵手势。
func TestSecondGestureAfterMultiTouchUsesItsOwnCoordinates(t *testing.T) {
	var b []byte
	// 第一轮：双指，结束时驱动 slot 停在 1
	b = append(b, ev(0, evKey, btnTouch, 1)...)
	b = append(b, ev(0, evAbs, absMTSlot, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 5)...)
	b = append(b, ev(0, evAbs, absMTX, 300)...)
	b = append(b, ev(0, evAbs, absMTY, 400)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	b = append(b, ev(0, evAbs, absMTSlot, 1)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 6)...)
	b = append(b, ev(0, evAbs, absMTX, 900)...)
	b = append(b, ev(0, evAbs, absMTY, 1200)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	b = append(b, ev(0, evAbs, absMTSlot, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, -1)...)
	b = append(b, ev(0, evAbs, absMTSlot, 1)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, -1)...)
	b = append(b, ev(0, evKey, btnTouch, 0)...)
	b = append(b, ev(0, evSyn, 0, 0)...)

	// 第二轮：单指点击竖版 (700,600) -> 横版 (847,700)
	b = append(b, ev(0, evKey, btnTouch, 1)...)
	b = append(b, ev(0, evAbs, absMTSlot, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, 7)...)
	b = append(b, ev(0, evAbs, absMTX, 700)...)
	b = append(b, ev(0, evAbs, absMTY, 600)...)
	b = append(b, ev(0, evSyn, 0, 0)...)
	b = append(b, ev(0, evAbs, absMTTrackingID, -1)...)
	b = append(b, ev(0, evKey, btnTouch, 0)...)
	b = append(b, ev(0, evSyn, 0, 0)...)

	got := feed(t, b)
	if len(got) != 2 {
		t.Fatalf("两轮各应产出 1 个事件，得到 %d: %+v", len(got), got)
	}
	// 第一轮是第一根手指的点击 (300,400) -> (1047,300)
	if got[0].Kind != KindTap || got[0].X != 1047 || got[0].Y != 300 {
		t.Fatalf("第一轮应产出 Tap(1047,300)，得到 %v(%d,%d)", got[0].Kind, got[0].X, got[0].Y)
	}
	// 第二次触摸必须用自己的坐标，不能再拿第一轮的 (300,400)。
	if got[1].Kind != KindTap || got[1].X != 847 || got[1].Y != 700 {
		t.Fatalf("第二次触摸应产出 Tap(847,700)，得到 %v(%d,%d)", got[1].Kind, got[1].X, got[1].Y)
	}
}

// 半截报文（长度不是 16 的倍数）不能崩，也不能产出垃圾事件。
func TestTruncatedStreamDoesNotPanic(t *testing.T) {
	full := tapStream(700, 600)
	for cut := 0; cut < len(full); cut++ {
		r := newParser(func(Event) {}, "ccw")
		if err := r.write(full[:cut]); err != nil {
			continue // 报错可以，崩不行
		}
	}
}

// 跨 write 的残片要拼起来，不能丢事件或让后续流错位。
func TestParserKeepsPartialEvent(t *testing.T) {
	full := tapStream(700, 600)
	var got []Event
	r := newParser(func(e Event) { got = append(got, e) }, "ccw")
	for i := 0; i < len(full); i += 7 {
		end := i + 7
		if end > len(full) {
			end = len(full)
		}
		if err := r.write(full[i:end]); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0].Kind != KindTap || got[0].X != 847 || got[0].Y != 700 {
		t.Fatalf("跨 write 残片应还原出 1 个 Tap (847,700)，得到 %+v", got)
	}
}

// Close 必须唤醒阻塞在 Read 上的读协程并关闭 Events channel。
// 用 FIFO 模拟 evdev 的阻塞语义：有写端但暂时没数据。
func TestReaderCloseStopsGoroutine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	// 先临时开读端让写端能打开，再关掉，把读到闭端的工作交给 Open。
	probe, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	w, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		probe.Close()
		t.Fatal(err)
	}
	probe.Close()
	defer w.Close()

	r, err := Open(path, "ccw")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // 让读协程进入阻塞的 Read

	// Close 之前 channel 不能已经关闭——否则「Close 后 1 秒内关闭」会假通过。
	select {
	case _, ok := <-r.Events():
		if !ok {
			t.Fatal("读协程在 Close 前就退出了")
		}
		t.Fatal("FIFO 没数据，不应收到事件")
	case <-time.After(20 * time.Millisecond):
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case _, ok := <-r.Events():
		if ok {
			t.Fatal("Close 后不应再收到事件")
		}
	case <-time.After(time.Second):
		t.Fatal("Close 后 1 秒内 Events channel 仍未关闭")
	}
}

func TestReaderCloseIsIdempotent(t *testing.T) {
	r, err := Open("/dev/null", "ccw")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("第一次 Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("第二次 Close: %v", err)
	}
}

func TestToLandscapeAllRotations(t *testing.T) {
	// ccw：RotateCCW 把横版 (lx,ly) 映射成竖版 (ly, 1447-lx)，
	// 所以逆向是 lx = 1447-py, ly = px。
	if lx, ly := ToLandscape(0, 0, "ccw"); lx != 1447 || ly != 0 {
		t.Fatalf("ccw 竖版左上角应映射到横版左下角，得到 (%d,%d)", lx, ly)
	}
	// cw：RotateCW 把横版 (lx,ly) 映射成竖版 (1071-ly, lx)，逆向 lx = py, ly = 1071-px。
	if lx, ly := ToLandscape(0, 0, "cw"); lx != 0 || ly != 1071 {
		t.Fatalf("cw 竖版左上角应映射到横版左上角之外，得到 (%d,%d)", lx, ly)
	}
}
