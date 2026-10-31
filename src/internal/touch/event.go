// Package touch 读 Kindle 的触摸设备，识别点击与四方向滑动。
//
// 设备实测（PW3 / 5.15.1.1，2026-10-31 抓包）：/dev/input/event1 是
// cyttsp4_mt，多点触控协议 B，报文里带 BTN_TOUCH / ABS_MT_TRACKING_ID /
// ABS_MT_POSITION_X / ABS_MT_POSITION_Y，坐标是竖版面板坐标（0..1071 / 0..1447）。
//
// Xorg 虽然开着这个设备，但没有 EVIOCGRAB，所以我们能同时读。
//
// 坐标映射（2026-10-31 在 PW3 上实测校准）：
//
//	竖版面板 (px,py)  →  横版画布 (1447-py, px)
//
// 校准方法：在设备上跑 cmd/spike/touchprobe，按提示操作，六项全部符合预期——
//
//	点左上角 → Tap x=59   y=86    （期望 ~56,  ~72）
//	点右下角 → Tap x=1373 y=1016  （期望 ~1392,~1072）
//	向上滑 → SwipeUp    向下滑 → SwipeDown
//	向左滑 → SwipeLeft  向右滑 → SwipeRight
//
// 所以方向判定不需要翻转。这条注释是有意写详细的：映射推导自
// display.RotateCCW 的公式，但公式推导和物理方向对不对得上，只有实测能证明。
package touch

// Kind 是识别出来的手势。
type Kind int

const (
	KindTap Kind = iota
	KindSwipeUp
	KindSwipeDown
	KindSwipeLeft
	KindSwipeRight
)

func (k Kind) String() string {
	return [...]string{"Tap", "SwipeUp", "SwipeDown", "SwipeLeft", "SwipeRight"}[k]
}

// Event 是一次被识别出来的手势，坐标已经换成横版画布坐标。
//
// X/Y 是抬指点，StartX/StartY 是落手点（按下后第一个 SYN_REPORT 锁定的坐标）。
// 命中判定必须用落手点：手指抬起前可能已经滑到另一个控件上 —— 例如从课程区
// 向上滑，抬手点会落进日历网格，用抬指点判定就会误选日期。
type Event struct {
	Kind Kind
	X, Y int // 抬指点
	// StartX/StartY 是落手点。Tap 与滑动都填。
	StartX, StartY int
}

// 手势判定阈值。
const (
	// tapMaxDist 内的位移算点击（像素）。
	tapMaxDist = 12
	// swipeMinDist 以上才算滑动。两者之间是死区：既不算点击也不算滑动，
	// 避免手指轻微抖动被误判成滑动。
	swipeMinDist = 40
)

// ToLandscape 把竖版面板坐标换成横版画布坐标。
//
// 依据 display.RotateCCW 的映射：横版 (lx,ly) → 竖版 (ly, W-1-lx)，
// W = 横版宽 1448。逆过来就是 lx = 1447-py, ly = px。
// 方向已按 2026-10-31 真机实测校准，见包注释。
func ToLandscape(px, py int, rotate string) (int, int) {
	switch rotate {
	case "cw":
		// RotateCW：横版 (lx,ly) → 竖版 (H-1-ly, lx)，H = 横版高 1072。
		// 逆向：lx = py, ly = 1071-px。
		return py, 1071 - px
	default: // ccw
		return 1447 - py, px
	}
}
