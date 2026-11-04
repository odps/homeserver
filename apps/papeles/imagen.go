package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // capturas y escaneos en PNG

	_ "golang.org/x/image/webp" // fotos en WebP
)

// Imágenes subidas: se descodifican y se vuelven a codificar en JPEG en el
// servidor. Así se va todo el EXIF (lugar GPS, modelo del móvil, hora) y lo
// que viniera escondido en el fichero. La orientación del EXIF se aplica
// antes; la transparencia se pinta sobre blanco. Mismo método que las fotos
// de progreso de Entreno.

const (
	maxPixels = 40e6 // por encima no se descodifica (bombas de descompresión)
	imageSide = 3000 // un escaneo se tiene que poder leer
	thumbSide = 400
	imageQ    = 88
	thumbQ    = 76
)

// reencode devuelve la imagen y su miniatura en JPEG, sin metadatos.
func reencode(raw []byte, kind string) (full, thumb []byte, w, h int, err error) {
	bad := fileErr("La imagen está dañada o no se puede leer.")
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return nil, nil, 0, 0, bad
	}
	if float64(cfg.Width)*float64(cfg.Height) > maxPixels || cfg.Width > 16000 || cfg.Height > 16000 {
		return nil, nil, 0, 0, fileErr("La imagen tiene demasiados píxeles (más de 40 megapíxeles).")
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, 0, 0, bad
	}
	o := 1
	if kind == "image/jpeg" {
		o = exifOrientation(raw)
	}
	big := applyOrientation(shrink(src, imageSide), o)
	small := shrink(big, thumbSide)
	var a, b bytes.Buffer
	if err := jpeg.Encode(&a, big, &jpeg.Options{Quality: imageQ}); err != nil {
		return nil, nil, 0, 0, err
	}
	if err := jpeg.Encode(&b, small, &jpeg.Options{Quality: thumbQ}); err != nil {
		return nil, nil, 0, 0, err
	}
	return a.Bytes(), b.Bytes(), big.Bounds().Dx(), big.Bounds().Dy(), nil
}

// pixels lee un píxel en RGB de 8 bits, ya sobre fondo blanco; rápido para
// los tipos más comunes.
func pixels(src image.Image) func(x, y int) (uint32, uint32, uint32) {
	switch m := src.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32) {
			yi, ci := m.YOffset(x, y), m.COffset(x, y)
			r, g, b := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			return uint32(r), uint32(g), uint32(b)
		}
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32) {
			i := m.PixOffset(x, y)
			a := uint32(m.Pix[i+3])
			blend := func(c uint8) uint32 { return (uint32(c)*a + 255*(255-a)) / 255 }
			return blend(m.Pix[i]), blend(m.Pix[i+1]), blend(m.Pix[i+2])
		}
	case *image.RGBA: // premultiplicado
		return func(x, y int) (uint32, uint32, uint32) {
			i := m.PixOffset(x, y)
			na := 255 - uint32(m.Pix[i+3])
			return uint32(m.Pix[i]) + na, uint32(m.Pix[i+1]) + na, uint32(m.Pix[i+2]) + na
		}
	}
	return func(x, y int) (uint32, uint32, uint32) {
		r, g, b, a := src.At(x, y).RGBA() // premultiplicado, 16 bits
		na := 0xffff - a
		return (r + na) >> 8, (g + na) >> 8, (b + na) >> 8
	}
}

// shrink reduce la imagen a como mucho side píxeles de lado largo, con la
// media de cada bloque (suficiente para fotos y escaneos; sin dependencias).
func shrink(src image.Image, side int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dw, dh := sw, sh
	if sw >= sh && sw > side {
		dw, dh = side, max(1, sh*side/sw)
	} else if sh > sw && sh > side {
		dw, dh = max(1, sw*side/sh), side
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	at := pixels(src)
	for y := 0; y < dh; y++ {
		y0, y1 := sb.Min.Y+y*sh/dh, sb.Min.Y+(y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0, x1 := sb.Min.X+x*sw/dw, sb.Min.X+(x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, n uint32
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb := at(sx, sy)
					r, g, b, n = r+pr, g+pg, b+pb, n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n), uint8(g/n), uint8(b/n), 255
		}
	}
	return dst
}

// applyOrientation aplica la orientación del EXIF (1 a 8).
func applyOrientation(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			copy(dst.Pix[dst.PixOffset(x, y):dst.PixOffset(x, y)+4], src.Pix[src.PixOffset(sx, sy):src.PixOffset(sx, sy)+4])
		}
	}
	return dst
}

// exifOrientation lee la etiqueta de orientación (0x0112) del EXIF de un
// JPEG. 1 si no hay o no se entiende.
func exifOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 || marker == 0xFF {
			i++
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // empieza la imagen: ya no hay EXIF
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[off:]))
	for k := 0; k < count; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
