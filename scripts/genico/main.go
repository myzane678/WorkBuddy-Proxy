// genico 用 Go 标准库手绘 admin 页同款 logo（靛蓝→紫渐变圆角方块 + 心电折线），
// 输出多尺寸 assets/icon.ico（桌面快捷方式用）与 assets/favicon32.png（base64 内嵌 admin 页 favicon）。
// 用法：在仓库根目录 go run ./scripts/genico
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const ss = 4 // 每轴超采样倍数（抗锯齿）

// 渐变端点色：#4f46e5 → #9333ea（与 admin.html 内 logo 一致）
var (
	c0 = [3]float64{79, 70, 229}
	c1 = [3]float64{147, 51, 234}
)

// 折线顶点：M6 14 L9.5 14 L11.5 8.5 L14.5 17 L16 14 L18 14（SVG 逻辑坐标 0-24）
var segs = [][2]float64{
	{6, 14}, {9.5, 14}, {11.5, 8.5}, {14.5, 17}, {16, 14}, {18, 14},
}

func grad(t float64) [3]float64 {
	return [3]float64{
		c0[0] + (c1[0]-c0[0])*t,
		c0[1] + (c1[1]-c0[1])*t,
		c0[2] + (c1[2]-c0[2])*t,
	}
}

// rectSDF 到圆角矩形（中心 12,12，半宽 10.5，圆角半径 6，描边 1.8）边界的有符号距离。
func rectSDF(x, y float64) float64 {
	const hw, hh, r = 10.5, 10.5, 6.0
	dx := math.Abs(x-12) - (hw - r)
	dy := math.Abs(y-12) - (hh - r)
	ox, oy := math.Max(dx, 0), math.Max(dy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(dx, dy), 0) - r
}

// segDist 到折线各线段的最近距离（描边 1.9）。
func segDist(x, y float64) float64 {
	best := math.Inf(1)
	for i := 0; i < len(segs)-1; i++ {
		ax, ay := segs[i][0], segs[i][1]
		bx, by := segs[i+1][0], segs[i+1][1]
		abx, aby := bx-ax, by-ay
		apx, apy := x-ax, y-ay
		len2 := abx*abx + aby*aby
		t := 0.0
		if len2 > 0 {
			t = math.Min(1, math.Max(0, (apx*abx+apy*aby)/len2))
		}
		if d := math.Hypot(apx-abx*t, apy-aby*t); d < best {
			best = d
		}
	}
	return best
}

// draw 按 SVG 逻辑坐标（0-24）绘制 size×size 的 logo。
func draw(size int) *image.RGBA {
	S := size * ss
	cover := make([]uint8, S*S)
	for py := 0; py < S; py++ {
		for px := 0; px < S; px++ {
			x := (float64(px) + 0.5) / float64(S) * 24
			y := (float64(py) + 0.5) / float64(S) * 24
			if math.Abs(rectSDF(x, y)) <= 0.9 || segDist(x, y) <= 0.95 {
				cover[py*S+px] = 1
			}
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			sum := 0
			for yy := 0; yy < ss; yy++ {
				for xx := 0; xx < ss; xx++ {
					sum += int(cover[(py*ss+yy)*S+px*ss+xx])
				}
			}
			// 像素中心处的渐变取色（对角线方向 t = (x+y)/48）
			cx := (float64(px)+0.5)/float64(size) * 24
			cy := (float64(py)+0.5)/float64(size) * 24
			c := grad((cx + cy) / 48)
			i := out.PixOffset(px, py)
			out.Pix[i] = uint8(c[0])
			out.Pix[i+1] = uint8(c[1])
			out.Pix[i+2] = uint8(c[2])
			out.Pix[i+3] = uint8(sum * 255 / (ss * ss))
		}
	}
	return out
}

// buildICO 把多张 RGBA 按 PNG 压缩格式打包成 .ico 容器。
func buildICO(imgs []*image.RGBA) ([]byte, error) {
	pngs := make([][]byte, len(imgs))
	for i, im := range imgs {
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			return nil, err
		}
		pngs[i] = b.Bytes()
	}
	out := make([]byte, 0, 6+16*len(pngs)+len(pngs[0])*4)
	head := make([]byte, 6)
	binary.LittleEndian.PutUint16(head[2:], 1) // type=icon
	binary.LittleEndian.PutUint16(head[4:], uint16(len(pngs)))
	out = append(out, head...)
	offset := uint32(6 + 16*len(pngs))
	for i, p := range pngs {
		w := imgs[i].Bounds().Dx()
		e := make([]byte, 16)
		if w < 256 {
			e[0], e[1] = uint8(w), uint8(w)
		} // 256 记 0
		binary.LittleEndian.PutUint16(e[4:], 1)  // planes
		binary.LittleEndian.PutUint16(e[6:], 32) // bpp
		binary.LittleEndian.PutUint32(e[8:], uint32(len(p)))
		binary.LittleEndian.PutUint32(e[12:], offset)
		out = append(out, e...)
		offset += uint32(len(p))
	}
	for _, p := range pngs {
		out = append(out, p...)
	}
	return out, nil
}

func main() {
	sizes := []int{16, 32, 48, 256}
	imgs := make([]*image.RGBA, len(sizes))
	for i, s := range sizes {
		imgs[i] = draw(s)
	}
	ico, err := buildICO(imgs)
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll("assets", 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join("assets", "icon.ico"), ico, 0o644); err != nil {
		panic(err)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, imgs[1]); err != nil { // 32px 给 favicon
		panic(err)
	}
	if err := os.WriteFile(filepath.Join("assets", "favicon32.png"), b.Bytes(), 0o644); err != nil {
		panic(err)
	}
	println("generated: assets/icon.ico (16/32/48/256) + assets/favicon32.png")
}
