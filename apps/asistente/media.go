package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Lo que no es texto: botones, fotos y documentos, y notas de voz.
//
//   - Botones: el modelo termina su respuesta con [[botones: Sí | No]] y aquí
//     se pintan bajo el mensaje. Pulsar uno es como escribir esa respuesta.
//   - Fotos e imágenes, y PDF: se mandan al modelo tal cual (decisión del
//     02-11-2026: lee mejor que un OCR, aunque la IA ve lo que haya en ellas).
//     En el historial queda solo «[foto]» y el pie, no la imagen.
//   - Notas de voz: se transcriben en el propio móvil con whisper.cpp; nada
//     sale del servidor. La transcripción sigue el camino de un texto.

type tgPhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int    `json:"file_size"`
}

type tgFileRef struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int    `json:"file_size"`
	Duration int    `json:"duration"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

const maxFileBytes = 15 << 20 // la Bot API no deja bajar más de 20 MB

// download baja un fichero que se ha mandado al bot.
func (t telegram) download(ctx context.Context, fileID string) ([]byte, error) {
	var f struct {
		FilePath string `json:"file_path"`
		FileSize int    `json:"file_size"`
	}
	if err := t.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	if f.FileSize > maxFileBytes || f.FilePath == "" {
		return nil, errors.New("el fichero es demasiado grande (más de 15 MB)")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", t.base+"/file/bot"+t.token+"/"+f.FilePath, nil)
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, errors.New("no se ha podido bajar el fichero de Telegram") // el error llevaría el token
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Telegram no da el fichero (%s)", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
}

// ---- Botones ----

var reButtons = regexp.MustCompile(`(?s)\n?\s*\[\[\s*botones\s*:\s*([^\]]+)\]\]\s*$`)

// splitButtons separa del texto la línea de botones (como mucho 4, de hasta
// 40 caracteres).
func splitButtons(reply string) (string, []string) {
	m := reButtons.FindStringSubmatchIndex(reply)
	if m == nil {
		return reply, nil
	}
	var opts []string
	for _, o := range strings.Split(reply[m[2]:m[3]], "|") {
		if o = strings.TrimSpace(o); o != "" && len(opts) < 4 {
			if r := []rune(o); len(r) > 40 {
				o = string(r[:40])
			}
			opts = append(opts, o)
		}
	}
	return strings.TrimSpace(reply[:m[0]]), opts
}

// sendButtons manda el texto con botones bajo el último trozo.
func (t telegram) sendButtons(ctx context.Context, chat int64, text string, opts []string) error {
	if len(opts) == 0 {
		return t.send(ctx, chat, text)
	}
	parts := splitMessage(plainText(text), 3500)
	for _, part := range parts[:len(parts)-1] {
		if err := t.send(ctx, chat, part); err != nil {
			return err
		}
	}
	var row []map[string]string
	for _, o := range opts {
		data := o
		for len(data) > 60 { // callback_data: 64 bytes como mucho
			data = string([]rune(data)[:len([]rune(data))-1])
		}
		row = append(row, map[string]string{"text": o, "callback_data": "b:" + data})
	}
	return t.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": parts[len(parts)-1],
		"link_preview_options": map[string]bool{"is_disabled": true},
		"reply_markup":         map[string]any{"inline_keyboard": [][]map[string]string{row}}}, nil)
}

// pressed atiende la pulsación de un botón: quita los botones del mensaje y
// sigue la conversación como si se hubiera escrito esa opción.
func (a *agent) pressed(ctx context.Context, tg telegram, c config, cb *tgCallback) {
	tg.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID}, nil)
	if c.TelegramUser == 0 || cb.From.ID != c.TelegramUser || cb.Message == nil || !strings.HasPrefix(cb.Data, "b:") {
		return
	}
	chat := cb.Message.Chat.ID
	tg.call(ctx, "editMessageReplyMarkup", map[string]any{"chat_id": chat, "message_id": cb.Message.MessageID,
		"reply_markup": map[string]any{"inline_keyboard": [][]map[string]string{}}}, nil)
	choice := strings.TrimPrefix(cb.Data, "b:")
	tg.send(ctx, chat, "👉 "+choice)
	a.respond(ctx, tg, c, chat, choice, nil)
}

// ---- Fotos y documentos ----

// attachment es lo que va al modelo junto al texto: una parte de contenido
// del formato de OpenAI (image_url o file).
func imagePart(mime string, data []byte) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]string{
		"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), "detail": "auto"}}
}

func pdfPart(name string, data []byte) map[string]any {
	return map[string]any{"type": "file", "file": map[string]string{
		"filename": name, "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(data)}}
}

// bestPhoto elige el tamaño más grande que no pase de 1600 px (suficiente
// para leer un documento sin gastar de más).
func bestPhoto(sizes []tgPhotoSize) tgPhotoSize {
	best := sizes[0]
	for _, s := range sizes {
		if max(s.Width, s.Height) <= 1600 && s.Width*s.Height >= best.Width*best.Height {
			best = s
		}
	}
	return best
}

// gotFile atiende una foto o un documento.
func (a *agent) gotFile(ctx context.Context, tg telegram, c config, m *tgMessage) {
	chat := m.Chat.ID
	var id, mime, name, label string
	switch {
	case len(m.Photo) > 0:
		id, mime, label = bestPhoto(m.Photo).FileID, "image/jpeg", "[foto]"
	case m.Document != nil && strings.HasPrefix(m.Document.MimeType, "image/"):
		id, mime, name, label = m.Document.FileID, m.Document.MimeType, m.Document.FileName, "[imagen: "+m.Document.FileName+"]"
	case m.Document != nil && m.Document.MimeType == "application/pdf":
		id, mime, name, label = m.Document.FileID, "application/pdf", m.Document.FileName, "[PDF: "+m.Document.FileName+"]"
	default:
		tg.send(ctx, chat, "De los documentos solo sé leer imágenes y PDF.")
		return
	}
	data, err := tg.download(ctx, id)
	if err != nil {
		tg.send(ctx, chat, "No he podido abrir el fichero: "+err.Error())
		return
	}
	part := imagePart(mime, data)
	if mime == "application/pdf" {
		part = pdfPart(orText(name, "documento.pdf"), data)
	}
	text := strings.TrimSpace(m.Caption)
	if text == "" {
		text = "Mira esto y dime qué es y si hay algo que convenga apuntar (una cita, un plazo, un gasto)."
	}
	a.respondWith(ctx, tg, c, chat, text, label, []map[string]any{part}, false)
}

// ---- Notas de voz ----

// transcriber pasa audio a texto con whisper.cpp en el propio móvil.
type transcriber struct {
	ffmpeg, whisper, model string
	tmp                    string // aquí, no en /tmp: en Android no siempre existe
	threads                int
}

func newTranscriber(dataDir string) transcriber {
	t := transcriber{ffmpeg: os.Getenv("FFMPEG_BIN"), whisper: os.Getenv("WHISPER_BIN"), model: os.Getenv("WHISPER_MODEL"),
		tmp: filepath.Join(dataDir, "tmp"), threads: 4}
	if t.ffmpeg == "" {
		t.ffmpeg = "ffmpeg"
		// El servicio puede arrancar sin el PATH de Termux.
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			t.ffmpeg = "/data/data/com.termux/files/usr/bin/ffmpeg"
		}
	}
	if t.whisper == "" {
		t.whisper = filepath.Join(dataDir, "whisper", "whisper-cli")
	}
	if t.model == "" {
		t.model = filepath.Join(dataDir, "whisper", "modelo.bin")
	}
	return t
}

var errNoWhisper = errors.New("no puedo escuchar notas de voz: falta instalar whisper en el móvil")

func (t transcriber) ready() bool {
	_, e1 := os.Stat(t.model)
	_, e2 := exec.LookPath(t.whisper)
	_, e3 := exec.LookPath(t.ffmpeg)
	return e1 == nil && e2 == nil && e3 == nil
}

// transcribe convierte el audio (OGG/Opus de Telegram u otro) a WAV de 16 kHz
// y lo transcribe en español. Los ficheros temporales se borran al acabar.
func (t transcriber) transcribe(ctx context.Context, audio []byte) (string, error) {
	if !t.ready() {
		return "", errNoWhisper
	}
	if err := os.MkdirAll(t.tmp, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(t.tmp, "voz-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	in, wav := filepath.Join(dir, "nota.ogg"), filepath.Join(dir, "nota.wav")
	if err := os.WriteFile(in, audio, 0o600); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, t.ffmpeg, "-nostdin", "-loglevel", "error", "-i", in, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav).CombinedOutput(); err != nil {
		return "", fmt.Errorf("no se ha podido convertir el audio: %s", strings.TrimSpace(string(out)))
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, t.whisper, "-m", t.model, "-l", "es", "-t", fmt.Sprint(t.threads), "-nt", "-np", "-f", wav)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("la nota es demasiado larga para transcribirla aquí")
		}
		return "", fmt.Errorf("whisper ha fallado: %s", lastLine(stderr.String()))
	}
	text := strings.Join(strings.Fields(stdout.String()), " ")
	if text == "" || text == "[BLANK_AUDIO]" {
		return "", errors.New("no he entendido nada en la nota")
	}
	return text, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// gotVoice transcribe una nota de voz (o un audio) y la contesta como texto.
func (a *agent) gotVoice(ctx context.Context, tg telegram, c config, m *tgMessage) {
	chat := m.Chat.ID
	f := m.Voice
	if f == nil {
		f = m.Audio
	}
	if f.Duration > 600 {
		tg.send(ctx, chat, "La nota es muy larga: como mucho 10 minutos.")
		return
	}
	if !a.voice.ready() {
		tg.send(ctx, chat, errNoWhisper.Error()+".")
		return
	}
	tg.typing(ctx, chat)
	data, err := tg.download(ctx, f.FileID)
	if err != nil {
		tg.send(ctx, chat, "No he podido bajar la nota: "+err.Error())
		return
	}
	text, err := a.voice.transcribe(ctx, data)
	if err != nil {
		tg.send(ctx, chat, err.Error()+".")
		return
	}
	tg.send(ctx, chat, "🎙 «"+text+"»")
	a.respondWith(ctx, tg, c, chat, text, "", nil, true)
}
