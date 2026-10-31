// Package render 把 state.Snapshot 画成一张 1448×1072 的横版位图。
//
// 这一层不感知 kindle、不感知 fbink、不感知帧缓冲。输入是纯数据，
// 输出是 image.Image。因此可以在任何机器上出图，用于预览与金像测试。
package render

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"sync"
	"syscall"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

// 四档灰。设计上刻意不用更多层级 —— 在 Kindle 的 16 级灰上，
// 相邻灰度容易糊成一片。
var (
	ColorBlack = color.Gray{Y: 0}
	ColorGray  = color.Gray{Y: 102} // #666
	ColorLight = color.Gray{Y: 179} // #B3B3B3
	ColorWhite = color.Gray{Y: 255}
)

// Style 选择字重。
type Style int

const (
	Regular Style = iota
	Bold
)

// Fonts 持有两份已解析的 TTF，并按 (字重, 像素尺寸) 缓存生成的字面。
//
// 为什么不直接存 font.Face：界面需要十余种字号，一次性全建会浪费
// 内存（设备只有 512MB），而每分钟重复解析 TTF 又太慢。缓存是折中。
type Fonts struct {
	regular *sfnt.Font
	bold    *sfnt.Font

	// maps 是需要 munmap 的映射，Close 时释放。
	// 平时不读它 —— sfnt.Font 内部已经持有了同一个切片，
	// 这里只是让「映射还在」这件事有个明确的所有者。
	maps []mapped

	mu    sync.Mutex
	cache map[faceKey]font.Face
}

// mapped 是一段通过 mmap 取得的只读文件映射。
type mapped struct {
	data []byte
}

type faceKey struct {
	style Style
	px    float64
}

// NewFonts 解析两个 TTF 文件。此时还不生成任何字面。
func NewFonts(regularPath, boldPath string) (*Fonts, error) {
	reg, regMap, err := parseFont(regularPath)
	if err != nil {
		return nil, fmt.Errorf("解析常规字体 %s: %w", regularPath, err)
	}
	bold, boldMap, err := parseFont(boldPath)
	if err != nil {
		munmap(regMap)
		return nil, fmt.Errorf("解析粗体字体 %s: %w", boldPath, err)
	}
	f := &Fonts{
		regular: reg,
		bold:    bold,
		cache:   make(map[faceKey]font.Face),
	}
	if regMap != nil {
		f.maps = append(f.maps, mapped{data: regMap})
	}
	if boldMap != nil {
		f.maps = append(f.maps, mapped{data: boldMap})
	}
	return f, nil
}

// NewFontsFromData 从内存中的 TTF 数据构造 Fonts。
//
// 生产代码用 NewFonts 读磁盘；测试用这个直接喂 gofont，
// 这样渲染测试不依赖设备上的 STHeiti。
func NewFontsFromData(regular, bold []byte) (*Fonts, error) {
	reg, err := sfnt.Parse(regular)
	if err != nil {
		return nil, fmt.Errorf("解析常规字体: %w", err)
	}
	boldF, err := sfnt.Parse(bold)
	if err != nil {
		return nil, fmt.Errorf("解析粗体字体: %w", err)
	}
	return &Fonts{regular: reg, bold: boldF, cache: make(map[faceKey]font.Face)}, nil
}

// NewFallbackFonts 用 Go 内置的 gofont 构造 Fonts。
//
// 用途：-preview 在没有配置文件或设备字体时仍能出图；测试同理。
// 粗体也用 goregular：回退字体的唯一目的是「有字可显示」，
// 预览与测试都不依赖字重差异，省一个 ~130KB 的内嵌资源。
func NewFallbackFonts() (*Fonts, error) {
	return NewFontsFromData(goregular.TTF, goregular.TTF)
}

// NewFontsFromDataForTest 供跨包测试使用（cmd/kindleclock 的测试需要它）。
func NewFontsFromDataForTest() (*Fonts, error) {
	return NewFallbackFonts()
}

// parseFont 读入并解析一个字体文件，返回字体和它背后的映射（没有映射时为 nil）。
func parseFont(path string) (*sfnt.Font, []byte, error) {
	data, mapped, err := mapOrRead(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := sfnt.Parse(data)
	if err != nil {
		if mapped {
			munmap(data)
		}
		return nil, nil, fmt.Errorf("不是有效的 TTF/OTF: %w", err)
	}
	if !mapped {
		return f, nil, nil
	}
	return f, data, nil
}

// mapOrRead 尽量用 mmap 把字体文件映射进地址空间，失败时退回整读。
//
// 为什么值得：sfnt.Parse 要一个 []byte 并会一直持有它。用 os.ReadFile
// 等于把整个 TTF 拷到堆上 —— 实测两份 STHeiti 合计占 20.2 MB RSS。
// 改成 mmap 之后页是**按需 fault-in 的可回收文件页**：实测同样取过
// 149 个字形轮廓，字体部分只占 3.5 MB，省下约 17 MB。
// 设备只有 512 MB 内存、没有 swap，这个差价不小。
//
// 退回路径必须保留：Kindle 的 /usr 是压缩只读文件系统，
// 不能假定它一定支持 mmap。退回时行为与原来完全一致。
func mapOrRead(path string) (data []byte, isMapped bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	if st, statErr := f.Stat(); statErr == nil &&
		st.Size() > 0 && st.Size() <= math.MaxInt32 {
		if b, mmErr := syscall.Mmap(int(f.Fd()), 0, int(st.Size()),
			syscall.PROT_READ, syscall.MAP_PRIVATE); mmErr == nil {
			return b, true, nil
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return b, false, nil
}

// munmap 释放一段映射。
func munmap(b []byte) {
	if b != nil {
		_ = syscall.Munmap(b)
	}
}

// Face 返回指定字重与像素尺寸的字面，结果被缓存。
//
// DPI 固定 72，使 Size 直接等于像素。生产用法在启动时就调用一遍
// 所有会用到的尺寸，把生成开销挪出主循环。
func (f *Fonts) Face(style Style, px float64) (font.Face, error) {
	key := faceKey{style: style, px: px}

	f.mu.Lock()
	defer f.mu.Unlock()

	if face, ok := f.cache[key]; ok {
		return face, nil
	}

	src := f.regular
	if style == Bold {
		src = f.bold
	}
	face, err := opentype.NewFace(src, &opentype.FaceOptions{
		Size:    px,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("生成 %v 字面 (%.0fpx): %w", style, px, err)
	}
	f.cache[key] = face
	return face, nil
}

// Close 释放所有缓存字面，以及字体文件的内存映射。
//
// 注意顺序：先关字面（它们引用字体数据），再 munmap。
// 关完之后这个 Fonts 不可再用 —— 生产代码在进程退出前调用一次。
func (f *Fonts) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	var first error
	for key, face := range f.cache {
		if err := face.Close(); err != nil && first == nil {
			first = err
		}
		delete(f.cache, key)
	}
	for _, m := range f.maps {
		munmap(m.data)
	}
	f.maps = nil
	// 字体对象本身也引用着被映射的内存，munmap 之后不能再碰。
	f.regular, f.bold = nil, nil
	return first
}
