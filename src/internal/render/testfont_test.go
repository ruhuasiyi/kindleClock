package render

import "golang.org/x/image/font/gofont/goregular"

// testFontData 返回 Go 自带的 gofont 数据。
// 用它而不是设备字体：测试要能在任何机器上跑。
func testFontData() []byte {
	return goregular.TTF
}
