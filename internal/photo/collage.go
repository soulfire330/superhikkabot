package photo

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// CollageOptions — размеры и качество сетки.
type CollageOptions struct {
	CellSize  int
	Gap       int
	Quality   int
	MaxImages int
}

func (o CollageOptions) withDefaults() CollageOptions {
	if o.CellSize <= 0 {
		o.CellSize = 320
	}
	if o.CellSize < 64 {
		o.CellSize = 64
	}
	if o.CellSize > 1024 {
		o.CellSize = 1024
	}
	if o.Gap <= 0 {
		o.Gap = 4
	}
	if o.Gap > 32 {
		o.Gap = 32
	}
	if o.Quality <= 0 {
		o.Quality = 80
	}
	if o.Quality > 100 {
		o.Quality = 100
	}
	if o.MaxImages <= 0 {
		o.MaxImages = 30
	}
	return o
}

// BuildCollages собирает одну или несколько сеток; больше MaxImages на коллаж не влезает.
func BuildCollages(images [][]byte, opts CollageOptions) ([][]byte, error) {
	opts = opts.withDefaults()
	if len(images) == 0 {
		return nil, fmt.Errorf("нет изображений для коллажа")
	}

	result := make([][]byte, 0, len(images)/opts.MaxImages+1)
	for start := 0; start < len(images); start += opts.MaxImages {
		end := min(start+opts.MaxImages, len(images))

		collage, err := buildOne(images[start:end], opts, start)
		if err != nil {
			return nil, err
		}
		result = append(result, collage)
	}

	return result, nil
}

func buildOne(images [][]byte, opts CollageOptions, offset int) ([]byte, error) {
	count := len(images)
	cols := int(math.Ceil(math.Sqrt(float64(count))))
	if cols < 1 {
		cols = 1
	}
	rows := (count + cols - 1) / cols
	if rows < 1 {
		rows = 1
	}

	width := cols*opts.CellSize + max(0, cols-1)*opts.Gap
	height := rows*opts.CellSize + max(0, rows-1)*opts.Gap

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{R: 18, G: 18, B: 18, A: 255}}, image.Point{}, xdraw.Src)

	decoded := 0
	for i, data := range images {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		decoded++

		cellX := (i % cols) * (opts.CellSize + opts.Gap)
		cellY := (i / cols) * (opts.CellSize + opts.Gap)

		bounds := img.Bounds()
		srcW, srcH := bounds.Dx(), bounds.Dy()
		if srcW <= 0 || srcH <= 0 {
			continue
		}

		scale := math.Min(float64(opts.CellSize)/float64(srcW), float64(opts.CellSize)/float64(srcH))
		dstW := max(1, int(math.Round(float64(srcW)*scale)))
		dstH := max(1, int(math.Round(float64(srcH)*scale)))
		dstX := cellX + (opts.CellSize-dstW)/2
		dstY := cellY + (opts.CellSize-dstH)/2

		xdraw.CatmullRom.Scale(canvas, image.Rect(dstX, dstY, dstX+dstW, dstY+dstH), img, bounds, xdraw.Over, nil)
		drawIndex(canvas, cellX+6, cellY+6, offset+i+1)
	}

	if decoded == 0 {
		return nil, fmt.Errorf("не удалось декодировать ни одного изображения")
	}

	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, canvas, &jpeg.Options{Quality: opts.Quality}); err != nil {
		return nil, fmt.Errorf("кодирование коллажа: %w", err)
	}
	return buffer.Bytes(), nil
}

// glyphs — цифры 3×5 точками: модель читает номера ячеек.
var glyphs = map[rune][5]uint8{
	'0': {0b111, 0b101, 0b101, 0b101, 0b111},
	'1': {0b010, 0b110, 0b010, 0b010, 0b111},
	'2': {0b111, 0b001, 0b111, 0b100, 0b111},
	'3': {0b111, 0b001, 0b111, 0b001, 0b111},
	'4': {0b101, 0b101, 0b111, 0b001, 0b001},
	'5': {0b111, 0b100, 0b111, 0b001, 0b111},
	'6': {0b111, 0b100, 0b111, 0b101, 0b111},
	'7': {0b111, 0b001, 0b001, 0b001, 0b001},
	'8': {0b111, 0b101, 0b111, 0b101, 0b111},
	'9': {0b111, 0b101, 0b111, 0b001, 0b111},
}

// drawIndex рисует номер ячейки в тёмном прямоугольнике.
func drawIndex(dst *image.RGBA, x, y, number int) {
	const (
		glyphWidth  = 3
		glyphHeight = 5
		pixel       = 4
		spacing     = 4
		padding     = 4
	)

	text := fmt.Sprintf("%d", number)
	width := len(text)*glyphWidth*pixel + max(0, len(text)-1)*spacing + padding*2
	height := glyphHeight*pixel + padding*2

	for py := 0; py < height; py++ {
		for px := 0; px < width; px++ {
			dst.SetRGBA(x+px, y+py, color.RGBA{R: 0, G: 0, B: 0, A: 200})
		}
	}

	penX := x + padding
	for _, symbol := range text {
		rows, ok := glyphs[symbol]
		if !ok {
			penX += glyphWidth*pixel + spacing
			continue
		}

		for row, bits := range rows {
			for col := 0; col < glyphWidth; col++ {
				if bits&(1<<(glyphWidth-1-col)) == 0 {
					continue
				}
				for dy := 0; dy < pixel; dy++ {
					for dx := 0; dx < pixel; dx++ {
						dst.SetRGBA(x+penX+col*pixel+dx, y+padding+row*pixel+dy, color.RGBA{R: 255, G: 255, B: 255, A: 255})
					}
				}
			}
		}

		penX += glyphWidth*pixel + spacing
	}
}
