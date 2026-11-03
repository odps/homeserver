package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Talia habla: la respuesta se lee en voz alta en el propio móvil (voces de
// Piper con sherpa-onnx) y se manda como nota de voz, además del texto. Nada
// sale del servidor. Por defecto contesta con voz cuando se le habla con voz;
// /voz siempre | auto | no lo cambia.

type speaker struct {
	bin, voiceDir, ffmpeg, tmp string
	sid, threads               int
}

func newSpeaker(dataDir string) speaker {
	s := speaker{bin: os.Getenv("TTS_BIN"), voiceDir: os.Getenv("TTS_VOICE"), ffmpeg: newTranscriber(dataDir).ffmpeg,
		tmp: filepath.Join(dataDir, "tmp"), threads: 4}
	if s.bin == "" {
		s.bin = filepath.Join(dataDir, "voz", "sherpa-onnx-offline-tts")
	}
	if s.voiceDir == "" {
		s.voiceDir = filepath.Join(dataDir, "voz", "voz")
	}
	// Hablante de la voz (las hay con varios): TTS_SPEAKER o el fichero
	// voz/hablante junto a la voz.
	sid := os.Getenv("TTS_SPEAKER")
	if sid == "" {
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(s.voiceDir), "hablante"))
		sid = strings.TrimSpace(string(b))
	}
	fmt.Sscan(sid, &s.sid)
	return s
}

// model es el .onnx de la voz (el único que haya en su carpeta).
func (s speaker) model() string {
	m, _ := filepath.Glob(filepath.Join(s.voiceDir, "*.onnx"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func (s speaker) ready() bool {
	_, e1 := exec.LookPath(s.bin)
	_, e2 := exec.LookPath(s.ffmpeg)
	_, e3 := os.Stat(filepath.Join(s.voiceDir, "tokens.txt"))
	return e1 == nil && e2 == nil && e3 == nil && s.model() != ""
}

var errNoVoice = errors.New("no puedo hablar: falta instalar la voz en el móvil")

const maxSpoken = 900 // caracteres: lo más largo, mejor leerlo

var (
	reURL    = regexp.MustCompile(`https?://\S+`)
	reSpaces = regexp.MustCompile(`[ \t]+`)
)

// speechText deja el texto listo para leerlo: sin enlaces, emojis ni
// viñetas, y recortado si es largo.
func speechText(s string) (string, bool) {
	s = reURL.ReplaceAllString(s, "(te dejo el enlace por escrito)")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '•' || r == '·':
			b.WriteRune(',')
		case r == '\n':
			b.WriteString(". ")
		case unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) || (r >= 0xFE00 && r <= 0xFE0F) || r == 0x200D:
			// emojis y sus modificadores: no se leen
		default:
			b.WriteRune(r)
		}
	}
	out := reSpaces.ReplaceAllString(b.String(), " ")
	out = strings.NewReplacer(". .", ".", ".. ", ". ", ". ,", ",", ":.", ":", "?.", "?", "!.", "!", ",.", ",").Replace(out)
	out = strings.Trim(out, " .,")
	cut := false
	if r := []rune(out); len(r) > maxSpoken {
		out = string(r[:maxSpoken])
		if i := strings.LastIndexAny(out, ".!?"); i > maxSpoken/2 {
			out = out[:i+1]
		}
		out += " El resto te lo dejo por escrito."
		cut = true
	}
	return out, cut
}

// say lee el texto y devuelve la nota de voz en OGG/Opus.
func (s speaker) say(ctx context.Context, text string) ([]byte, error) {
	if !s.ready() {
		return nil, errNoVoice
	}
	if err := os.MkdirAll(s.tmp, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(s.tmp, "habla-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	wav, ogg := filepath.Join(dir, "voz.wav"), filepath.Join(dir, "voz.ogg")
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.bin, "--vits-model="+s.model(), "--vits-tokens="+filepath.Join(s.voiceDir, "tokens.txt"),
		"--vits-data-dir="+filepath.Join(s.voiceDir, "espeak-ng-data"), fmt.Sprintf("--num-threads=%d", s.threads),
		fmt.Sprintf("--sid=%d", s.sid), "--output-filename="+wav, text)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("la voz ha fallado: %s", lastLine(stderr.String()))
	}
	if out, err := exec.CommandContext(ctx, s.ffmpeg, "-nostdin", "-loglevel", "error", "-i", wav, "-c:a", "libopus", "-b:a", "32k", ogg).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("no se ha podido preparar la nota de voz: %s", strings.TrimSpace(string(out)))
	}
	return os.ReadFile(ogg)
}

// sendVoice manda una nota de voz (OGG/Opus).
func (t telegram) sendVoice(ctx context.Context, chat int64, ogg []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("chat_id", fmt.Sprint(chat))
	part, _ := w.CreateFormFile("voice", "talia.ogg")
	part.Write(ogg)
	w.Close()
	req, _ := http.NewRequestWithContext(ctx, "POST", t.base+"/bot"+t.token+"/sendVoice", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := t.http.Do(req)
	if err != nil {
		return errors.New("Telegram no responde al mandar la nota de voz") // el error llevaría el token
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram rechaza la nota de voz (%s)", resp.Status)
	}
	return nil
}

// ---- Cuándo habla ----

const (
	voiceAuto   = "auto"    // con voz si se le ha hablado con voz
	voiceAlways = "siempre" // siempre
	voiceNever  = "no"      // nunca
)

func (s store) voiceMode(ctx context.Context) string {
	var v string
	s.db.QueryRow(ctx, `SELECT value FROM kv WHERE key = 'voz_modo'`).Scan(&v)
	if v != voiceAlways && v != voiceNever {
		return voiceAuto
	}
	return v
}

func (s store) setVoiceMode(ctx context.Context, mode string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO kv (key, value) VALUES ('voz_modo', $1)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, mode)
	return err
}

// voiceCommand atiende /voz [siempre|auto|no].
func (a *agent) voiceCommand(ctx context.Context, arg string) string {
	arg = strings.ToLower(strings.TrimSpace(arg))
	switch arg {
	case "":
	case "siempre", "si", "sí", "on":
		a.store.setVoiceMode(ctx, voiceAlways)
	case "auto", "automatico", "automático":
		a.store.setVoiceMode(ctx, voiceAuto)
	case "no", "nunca", "off":
		a.store.setVoiceMode(ctx, voiceNever)
	default:
		return "Usa /voz siempre, /voz auto o /voz no."
	}
	mode := map[string]string{voiceAuto: "te contesto con voz cuando me hablas con voz", voiceAlways: "te contesto siempre también con voz",
		voiceNever: "no te contesto con voz"}[a.store.voiceMode(ctx)]
	if !a.speak.ready() {
		mode += " (aunque ahora " + strings.TrimPrefix(errNoVoice.Error(), "no puedo hablar: ") + ")"
	}
	return "🔊 Ahora " + mode + ". Cámbialo con /voz siempre, /voz auto o /voz no."
}

// speakReply manda la respuesta también como nota de voz, si toca.
func (a *agent) speakReply(ctx context.Context, tg telegram, chat int64, reply string, voiceIn bool) {
	switch a.store.voiceMode(ctx) {
	case voiceNever:
		return
	case voiceAuto:
		if !voiceIn {
			return
		}
	}
	if !a.speak.ready() {
		return
	}
	text, _ := speechText(reply)
	if text == "" {
		return
	}
	tg.call(ctx, "sendChatAction", map[string]any{"chat_id": chat, "action": "record_voice"}, nil)
	ogg, err := a.speak.say(ctx, text)
	if err != nil {
		a.log.Warn("voz", "err", err)
		return
	}
	if err := tg.sendVoice(ctx, chat, ogg); err != nil {
		a.log.Warn("voz", "err", err)
	}
}
