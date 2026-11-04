package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // se aceptan PNG (capturas)
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/seal"
)

// Fotos de progreso. Son de lo más delicado de la app, así que:
//   - Se vuelven a codificar en el servidor: se va todo el EXIF (lugar GPS,
//     modelo del móvil, hora) y lo que viniera escondido en el fichero. La
//     orientación del EXIF se aplica antes de quitarlo.
//   - Se reducen (1600 px el lado largo, y una miniatura de 480).
//   - Se guardan cifradas (platform/seal) en DATA_DIR/fotos, con el id en el
//     contexto: un fichero no se descifra como si fuera otro.
//   - Solo se sirven a su dueño, sin caché. No pasan nunca por la IA.

const (
	sealPhoto   = "entreno/photos"
	maxPhoto    = 15 << 20 // lo que sube el móvil
	maxPixels   = 50e6     // por encima, no se descodifica (bombas de descompresión)
	photoSide   = 1600
	thumbSide   = 480
	photoQ      = 85
	thumbQ      = 78
	maxPhotoDay = 12 // fotos por día
)

var poses = []option{{"frente", "Frente"}, {"perfil", "Perfil"}, {"espalda", "Espalda"}, {"otra", "Otra"}}

func poseLabel(k string) string {
	for _, p := range poses {
		if p.Key == k {
			return p.Name
		}
	}
	return "Otra"
}

var rePhotoID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// photoErr es un error de la foto que se puede enseñar tal cual a la persona
// (los demás, de disco o de base de datos, no).
type photoErr string

func (e photoErr) Error() string { return string(e) }

// photo es una foto (sin la imagen).
type photo struct {
	ID   string    `json:"-"`
	Day  time.Time `json:"-"`
	Pose string    `json:"pose"`
	Note string    `json:"nota,omitempty"`
	W    int       `json:"ancho"`
	H    int       `json:"alto"`
}

// photoFiles guarda las imágenes cifradas en un directorio.
type photoFiles struct {
	dir string
	box *seal.Box
}

func newPhotoFiles(dataDir string, box *seal.Box) *photoFiles {
	return &photoFiles{dir: filepath.Join(dataDir, "fotos"), box: box}
}

func (f *photoFiles) path(id string, mini bool) string {
	if mini {
		return filepath.Join(f.dir, id+".mini")
	}
	return filepath.Join(f.dir, id+".foto")
}

func photoContext(id string, mini bool) string {
	if mini {
		return "entreno/foto/" + id + "/mini"
	}
	return "entreno/foto/" + id
}

func (f *photoFiles) write(id string, mini bool, img []byte) error {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(f.box.Seal(photoContext(id, mini), img)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path(id, mini))
}

func (f *photoFiles) read(id string, mini bool) ([]byte, error) {
	b, err := os.ReadFile(f.path(id, mini))
	if err != nil {
		return nil, err
	}
	return f.box.Open(photoContext(id, mini), b)
}

func (f *photoFiles) remove(id string) {
	os.Remove(f.path(id, false))
	os.Remove(f.path(id, true))
}

// ---- Base de datos ----

// savePhoto procesa la imagen subida y la guarda.
func (s *service) savePhoto(ctx context.Context, owner string, day time.Time, pose, note string, raw []byte) (string, error) {
	if s.photos == nil {
		return "", photoErr("las fotos no están disponibles")
	}
	if !validPose(pose) {
		pose = "otra"
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 200 {
		return "", photoErr("la nota pasa de 200 caracteres")
	}
	var n int
	if err := s.store.db.QueryRow(ctx, `SELECT count(*) FROM photos WHERE owner = $1 AND day = $2`, owner, day.Format("2006-01-02")).Scan(&n); err != nil {
		return "", err
	}
	if n >= maxPhotoDay {
		return "", photoErr("ya hay muchas fotos ese día: borra alguna antes")
	}
	full, thumb, w, h, err := processPhoto(raw)
	if err != nil {
		return "", err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	if err := s.photos.write(id, false, full); err != nil {
		return "", err
	}
	if err := s.photos.write(id, true, thumb); err != nil {
		s.photos.remove(id)
		return "", err
	}
	sealed, err := s.store.box.SealJSON(sealPhoto, photo{Pose: pose, Note: note, W: w, H: h})
	if err != nil {
		s.photos.remove(id)
		return "", err
	}
	if _, err := s.store.db.Exec(ctx, `INSERT INTO photos (id, owner, day, sealed) VALUES ($1, $2, $3, $4)`,
		id, owner, day.Format("2006-01-02"), sealed); err != nil {
		s.photos.remove(id)
		return "", err
	}
	return id, nil
}

func validPose(p string) bool {
	for _, x := range poses {
		if x.Key == p {
			return true
		}
	}
	return false
}

// photoList devuelve las fotos de la persona, de la más reciente a la más
// antigua.
func (s store) photoList(ctx context.Context, owner string) ([]photo, error) {
	rows, err := s.db.Query(ctx, `SELECT id, day, sealed FROM photos WHERE owner = $1 ORDER BY day DESC, created_at DESC LIMIT 1000`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []photo
	for rows.Next() {
		var p photo
		var sealed []byte
		if err := rows.Scan(&p.ID, &p.Day, &sealed); err != nil {
			return nil, err
		}
		id, day := p.ID, p.Day
		if err := s.box.OpenJSON(sealPhoto, sealed, &p); err != nil {
			return nil, err
		}
		p.ID, p.Day = id, day
		out = append(out, p)
	}
	return out, rows.Err()
}

// photoMeta es una foto de la persona (errNotFound si no es suya).
func (s store) photoMeta(ctx context.Context, owner, id string) (photo, error) {
	if !rePhotoID.MatchString(id) {
		return photo{}, errNotFound
	}
	var p photo
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT day, sealed FROM photos WHERE id = $1 AND owner = $2`, id, owner).Scan(&p.Day, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return photo{}, errNotFound
	}
	if err != nil {
		return photo{}, err
	}
	day := p.Day
	if err := s.box.OpenJSON(sealPhoto, sealed, &p); err != nil {
		return photo{}, err
	}
	p.ID, p.Day = id, day
	return p, nil
}

// photoImage es la imagen (o la miniatura) de una foto de la persona.
func (s *service) photoImage(ctx context.Context, owner, id string, mini bool) ([]byte, error) {
	if _, err := s.store.photoMeta(ctx, owner, id); err != nil {
		return nil, err
	}
	return s.photos.read(id, mini)
}

func (s *service) deletePhoto(ctx context.Context, owner, id string) error {
	if !rePhotoID.MatchString(id) {
		return errNotFound
	}
	tag, err := s.store.db.Exec(ctx, `DELETE FROM photos WHERE id = $1 AND owner = $2`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	s.photos.remove(id)
	return nil
}

// ---- Imagen ----

// processPhoto comprueba la imagen, la endereza, la reduce y la vuelve a
// codificar en JPEG (sin metadatos). Devuelve la foto y la miniatura.
func processPhoto(raw []byte) (full, thumb []byte, w, h int, err error) {
	bad := photoErr("no es una foto JPEG o PNG válida")
	if len(raw) > maxPhoto {
		return nil, nil, 0, 0, photoErr("la foto pasa de 15 MB")
	}
	switch http.DetectContentType(raw) {
	case "image/jpeg", "image/png":
	default:
		return nil, nil, 0, 0, bad
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return nil, nil, 0, 0, bad
	}
	if float64(cfg.Width)*float64(cfg.Height) > maxPixels || cfg.Width > 16000 || cfg.Height > 16000 {
		return nil, nil, 0, 0, photoErr("la foto es demasiado grande")
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, 0, 0, bad
	}
	big := applyOrientation(shrink(src, photoSide), exifOrientation(raw))
	small := shrink(big, thumbSide)
	var a, b bytes.Buffer
	if err := jpeg.Encode(&a, big, &jpeg.Options{Quality: photoQ}); err != nil {
		return nil, nil, 0, 0, err
	}
	if err := jpeg.Encode(&b, small, &jpeg.Options{Quality: thumbQ}); err != nil {
		return nil, nil, 0, 0, err
	}
	return a.Bytes(), b.Bytes(), big.Bounds().Dx(), big.Bounds().Dy(), nil
}

// pixels lee un píxel en RGB de 8 bits, rápido para los JPEG (YCbCr).
func pixels(src image.Image) func(x, y int) (uint32, uint32, uint32) {
	switch m := src.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32) {
			yi, ci := m.YOffset(x, y), m.COffset(x, y)
			r, g, b := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			return uint32(r), uint32(g), uint32(b)
		}
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32) {
			i := m.PixOffset(x, y)
			return uint32(m.Pix[i]), uint32(m.Pix[i+1]), uint32(m.Pix[i+2])
		}
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32) {
			i := m.PixOffset(x, y)
			return uint32(m.Pix[i]), uint32(m.Pix[i+1]), uint32(m.Pix[i+2])
		}
	}
	return func(x, y int) (uint32, uint32, uint32) {
		r, g, b, _ := src.At(x, y).RGBA()
		return r >> 8, g >> 8, b >> 8
	}
}

// shrink reduce la imagen a como mucho side píxeles de lado largo, con la
// media de cada bloque (suficiente para fotos; sin dependencias).
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
