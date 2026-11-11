package photo

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}

	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, nil); err != nil {
		t.Fatalf("jpeg: %v", err)
	}
	return buffer.Bytes()
}

func TestBuildCollagesMakesASquareGrid(t *testing.T) {
	images := [][]byte{
		testJPEG(t, 200, 100),
		testJPEG(t, 100, 200),
		testJPEG(t, 150, 150),
		testJPEG(t, 64, 64),
	}

	collages, err := BuildCollages(images, CollageOptions{CellSize: 100, Quality: 70})
	if err != nil {
		t.Fatalf("BuildCollages: %v", err)
	}
	if len(collages) != 1 {
		t.Fatalf("коллажей %d, ждали 1", len(collages))
	}

	decoded, _, err := image.Decode(bytes.NewReader(collages[0]))
	if err != nil {
		t.Fatalf("коллаж не декодируется: %v", err)
	}

	// 4 картинки → сетка 2×2 по 100 px.
	bounds := decoded.Bounds()
	if bounds.Dx() < 204 || bounds.Dy() < 204 {
		t.Errorf("размер коллажа %dx%d, ждали не меньше 204x204", bounds.Dx(), bounds.Dy())
	}
}

func TestBuildCollagesSplitsByMaxImages(t *testing.T) {
	images := make([][]byte, 5)
	for i := range images {
		images[i] = testJPEG(t, 32, 32)
	}

	collages, err := BuildCollages(images, CollageOptions{CellSize: 64, MaxImages: 2})
	if err != nil {
		t.Fatalf("BuildCollages: %v", err)
	}
	if len(collages) != 3 {
		t.Fatalf("коллажей %d, ждали 3", len(collages))
	}
}

func TestBuildCollagesEmpty(t *testing.T) {
	if _, err := BuildCollages(nil, CollageOptions{}); err == nil {
		t.Error("для пустого набора ждали ошибку")
	}
}
