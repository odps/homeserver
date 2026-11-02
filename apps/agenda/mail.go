package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset" // correos en ISO-8859-1, Windows-1252…
	"github.com/emersion/go-message/mail"
	"github.com/jackc/pgx/v5"

	sealpkg "homeserver/platform/seal"
)

// Correo: se lee la bandeja de entrada por IMAP cada pocos minutos y se
// guarda cifrado en la base (para buscar, resumir y que el asistente lo lea),
// clasificado por sus metadatos (classify.go). Nunca se envía nada: como mucho
// se deja un borrador en la carpeta Borradores.
//
// Gmail (y cualquier IMAP) entra con una contraseña de aplicación; Outlook y
// Hotmail, con OAuth de Microsoft (flujo de código de dispositivo).

const (
	syncDays    = 30        // la primera vez se traen los de los últimos 30 días
	keepDays    = 120       // los más antiguos se borran de aquí (no del buzón)
	maxFirst    = 400       // como mucho, en la primera sincronización
	maxRawBytes = 4 << 20   // los más grandes se guardan sin cuerpo
	maxBody     = 100 << 10 // texto guardado por correo
)

type mailAccount struct {
	ID          int64
	Owner       string
	Kind        string
	Address     string
	Host        string
	Port        int
	Username    string
	Secret      []byte
	OAuthClient string
	Drafts      string
	UIDValidity int64
	LastUID     int64
	SyncedAt    *time.Time
	Error       string
}

type message struct {
	ID          int64       `json:"id"`
	AccountID   int64       `json:"-"`
	Account     string      `json:"cuenta"`
	UID         int64       `json:"-"`
	MessageID   string      `json:"-"`
	Refs        string      `json:"-"`
	FromName    string      `json:"de_nombre,omitempty"`
	FromAddr    string      `json:"de"`
	To          string      `json:"para,omitempty"`
	Cc          string      `json:"cc,omitempty"`
	Subject     string      `json:"asunto"`
	Date        time.Time   `json:"fecha"`
	Body        string      `json:"texto,omitempty"`
	Attachments []string    `json:"adjuntos,omitempty"`
	Seen        bool        `json:"leido"`
	Flagged     bool        `json:"destacado,omitempty"`
	Category    string      `json:"categoria"`
	Reason      string      `json:"motivo,omitempty"`
	Signals     mailSignals `json:"-"`
}

// mailBlob es lo que va cifrado de cada correo.
type mailBlob struct {
	MessageID   string      `json:"mid,omitempty"`
	Refs        string      `json:"refs,omitempty"`
	FromName    string      `json:"from_name,omitempty"`
	FromAddr    string      `json:"from,omitempty"`
	To          string      `json:"to,omitempty"`
	Cc          string      `json:"cc,omitempty"`
	Subject     string      `json:"subject,omitempty"`
	Body        string      `json:"body,omitempty"`
	Attachments []string    `json:"att,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	Signals     mailSignals `json:"signals"`
}

// ---- Base de datos ----

const accountCols = `id, owner, kind, address, host, port, username, secret, oauth_client, drafts, uidvalidity, last_uid, synced_at, error`

func (s store) accounts(ctx context.Context, owner string) ([]mailAccount, error) {
	q, args := `SELECT `+accountCols+` FROM mail_accounts ORDER BY id`, []any{}
	if owner != "" {
		q, args = `SELECT `+accountCols+` FROM mail_accounts WHERE owner = $1 ORDER BY id`, []any{owner}
	}
	rows, _ := s.db.Query(ctx, q, args...)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[mailAccount])
}

func (s store) account(ctx context.Context, owner string, id int64) (mailAccount, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+accountCols+` FROM mail_accounts WHERE owner = $1 AND id = $2`, owner, id)
	a, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[mailAccount])
	if errors.Is(err, pgx.ErrNoRows) {
		return a, errNotFound
	}
	return a, err
}

func (s store) addAccount(ctx context.Context, a mailAccount) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO mail_accounts (owner, kind, address, host, port, username, secret, oauth_client)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (owner, address) DO UPDATE SET kind = $2, host = $4, port = $5, username = $6, secret = $7,
			oauth_client = $8, error = ''
		RETURNING id`, a.Owner, a.Kind, strings.ToLower(a.Address), a.Host, a.Port, a.Username, a.Secret, a.OAuthClient).Scan(&id)
	return id, err
}

func (s store) deleteAccount(ctx context.Context, owner string, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM mail_accounts WHERE owner = $1 AND id = $2`, owner, id)
	return err
}

// accountSynced guarda hasta dónde se ha leído. Si falla, la siguiente vuelta
// volvería a descargarlo todo: se devuelve el error para que conste.
func (s store) accountSynced(ctx context.Context, a mailAccount, syncErr error) error {
	msg := ""
	if syncErr != nil {
		msg = validText(syncErr.Error())
	}
	_, err := s.db.Exec(ctx, `UPDATE mail_accounts SET uidvalidity = $2, last_uid = $3, drafts = $4, secret = $5, error = $6,
		synced_at = CASE WHEN $6 = '' THEN now() ELSE synced_at END WHERE id = $1`,
		a.ID, a.UIDValidity, a.LastUID, validText(a.Drafts), a.Secret, msg)
	return err
}

func (s store) saveMessage(ctx context.Context, m message) error {
	sealed, err := s.box.SealJSON(sealMessage, mailBlob{MessageID: m.MessageID, Refs: m.Refs, FromName: m.FromName,
		FromAddr: m.FromAddr, To: m.To, Cc: m.Cc, Subject: m.Subject, Body: m.Body, Attachments: m.Attachments,
		Reason: m.Reason, Signals: m.Signals})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO messages (account_id, uid, sent_at, seen, flagged, category, sealed, tokens)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (account_id, uid) DO NOTHING`,
		m.AccountID, m.UID, m.Date, m.Seen, m.Flagged, m.Category, sealed, mailTokens(s.box, m))
	return err
}

// mailTokens son las huellas para buscar: "f" en el remitente; "t" en todo
// (remitente, destinatarios, asunto y texto, por ese orden por si se llega al
// tope).
func mailTokens(box *sealpkg.Box, m message) []int64 {
	t := box.Index(nil, "f", m.FromName+" "+m.FromAddr)
	for _, s := range []string{m.FromName + " " + m.FromAddr, m.Subject, m.To + " " + m.Cc, m.Body} {
		t = box.Index(t, "t", s)
	}
	return t
}

const messageCols = `m.id, m.account_id, a.address, m.uid, m.sent_at, m.seen, m.flagged, m.category, m.sealed`

func (s store) messages(ctx context.Context, where string, args ...any) ([]message, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+messageCols+` FROM messages m JOIN mail_accounts a ON a.id = m.account_id WHERE `+where, args...)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (message, error) {
		var m message
		var sealed []byte
		if err := r.Scan(&m.ID, &m.AccountID, &m.Account, &m.UID, &m.Date, &m.Seen, &m.Flagged, &m.Category, &sealed); err != nil {
			return m, err
		}
		var b mailBlob
		if err := s.box.OpenJSON(sealMessage, sealed, &b); err != nil {
			return m, err
		}
		m.MessageID, m.Refs, m.FromName, m.FromAddr, m.To, m.Cc = b.MessageID, b.Refs, b.FromName, b.FromAddr, b.To, b.Cc
		m.Subject, m.Body, m.Attachments, m.Reason, m.Signals = b.Subject, b.Body, b.Attachments, b.Reason, b.Signals
		if m.Attachments == nil {
			m.Attachments = []string{}
		}
		return m, nil
	})
}

func (s store) messageByID(ctx context.Context, owner string, id int64) (message, error) {
	list, err := s.messages(ctx, `a.owner = $1 AND m.id = $2`, owner, id)
	if err != nil {
		return message{}, err
	}
	if len(list) == 0 {
		return message{}, errNotFound
	}
	return list[0], nil
}

type mailQuery struct {
	Text       string
	From       string
	UnreadOnly bool
	Since      time.Time
	Account    int64
	Categories []string // vacío: todas
	Limit      int
}

func (s store) searchMessages(ctx context.Context, owner string, q mailQuery) ([]message, error) {
	where, args := s.mailWhere(owner, q)
	if len(q.Categories) > 0 {
		args = append(args, q.Categories)
		where += fmt.Sprintf(` AND m.category = ANY($%d)`, len(args))
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 30
	}
	return s.messages(ctx, where+fmt.Sprintf(` ORDER BY m.sent_at DESC LIMIT %d`, q.Limit), args...)
}

// countMessages cuenta por categoría lo que encaja con q (sin mirar
// q.Categories).
func (s store) countMessages(ctx context.Context, owner string, q mailQuery) (map[string]int, error) {
	where, args := s.mailWhere(owner, q)
	rows, _ := s.db.Query(ctx, `SELECT m.category, count(*) FROM messages m JOIN mail_accounts a ON a.id = m.account_id
		WHERE `+where+` GROUP BY m.category`, args...)
	out := map[string]int{}
	var c string
	var n int
	_, err := pgx.ForEachRow(rows, []any{&c, &n}, func() error { out[c] = n; return nil })
	return out, err
}

func (s store) mailWhere(owner string, q mailQuery) (string, []any) {
	where, args := `a.owner = $1`, []any{owner}
	if t := s.box.Query("t", q.Text); len(t) > 0 {
		args = append(args, t)
		where += fmt.Sprintf(` AND m.tokens @> $%d`, len(args))
	}
	if t := s.box.Query("f", q.From); len(t) > 0 {
		args = append(args, t)
		where += fmt.Sprintf(` AND m.tokens @> $%d`, len(args))
	}
	if q.UnreadOnly {
		where += ` AND NOT m.seen`
	}
	if !q.Since.IsZero() {
		args = append(args, q.Since)
		where += fmt.Sprintf(` AND m.sent_at >= $%d`, len(args))
	}
	if q.Account != 0 {
		args = append(args, q.Account)
		where += fmt.Sprintf(` AND m.account_id = $%d`, len(args))
	}
	return where, args
}

// reclassify vuelve a clasificar el correo de una cuenta (al cambiar sus
// remitentes importantes o a ignorar).
func (s store) reclassify(ctx context.Context, owner string) error {
	p, err := s.prefs(ctx, owner)
	if err != nil {
		return err
	}
	list, err := s.messages(ctx, `a.owner = $1`, owner)
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, m := range list {
		was := m.Category
		classify(&m, p)
		if m.Category != was {
			batch.Queue(`UPDATE messages SET category = $2 WHERE id = $1`, m.ID, m.Category)
		}
	}
	return s.db.SendBatch(ctx, batch).Close()
}

// ---- IMAP ----

type mailer struct {
	store  store
	seal   *sealer
	log    *slog.Logger
	http   *http.Client
	msAuth string // https://login.microsoftonline.com/consumers (sustituible en los tests)
	dial   func(a mailAccount) (*imapclient.Client, error)
	mu     sync.Map // id de cuenta → *sync.Mutex: una conexión a la vez por cuenta
	flows  sync.Map // usuario → *deviceFlow
}

func newMailer(s store, seal *sealer, log *slog.Logger) *mailer {
	m := &mailer{store: s, seal: seal, log: log, http: &http.Client{Timeout: 30 * time.Second},
		msAuth: "https://login.microsoftonline.com/consumers"}
	m.dial = func(a mailAccount) (*imapclient.Client, error) {
		return imapclient.DialTLS(net.JoinHostPort(a.Host, fmt.Sprint(a.Port)), &imapclient.Options{Dialer: &net.Dialer{Timeout: 30 * time.Second}})
	}
	return m
}

func (m *mailer) lock(id int64) func() {
	v, _ := m.mu.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// xoauth2 es el SASL de Google y Microsoft para entrar con un token OAuth.
type xoauth2 struct{ user, token string }

func (x xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

// Si el token no vale, el servidor manda un JSON con el error y espera una
// respuesta vacía para dar el NO.
func (x xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }

// connect abre la sesión IMAP; corta si ctx acaba.
func (m *mailer) connect(ctx context.Context, a *mailAccount) (*imapclient.Client, error) {
	c, err := m.dial(*a)
	if err != nil {
		return nil, fmt.Errorf("no se puede conectar con %s: %w", a.Host, err)
	}
	context.AfterFunc(ctx, func() { c.Close() })
	secret, err := m.seal.open(a.Secret)
	if err != nil {
		c.Close()
		return nil, err
	}
	if a.Kind == "outlook" {
		token, refresh, err := m.msToken(ctx, a.OAuthClient, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {secret}})
		if err != nil {
			c.Close()
			return nil, err
		}
		if refresh != "" && refresh != secret {
			a.Secret = m.seal.seal(refresh) // Microsoft rota el refresh token
		}
		err = c.Authenticate(xoauth2{a.Username, token})
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("Outlook no acepta el acceso: %w", err)
		}
		return c, nil
	}
	if err := c.Login(a.Username, secret).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("el servidor no acepta el usuario o la contraseña de aplicación: %w", err)
	}
	return c, nil
}

// syncAccount trae lo nuevo de la bandeja de entrada y actualiza leídos y borrados.
func (m *mailer) syncAccount(ctx context.Context, a mailAccount) (int, error) {
	defer m.lock(a.ID)()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	n, err := m.syncInbox(ctx, &a)
	if serr := m.store.accountSynced(context.WithoutCancel(ctx), a, err); serr != nil {
		m.log.Error("correo: no se ha guardado hasta dónde se ha leído", "cuenta", a.Address, "err", serr)
	}
	return n, err
}

func (m *mailer) syncInbox(ctx context.Context, a *mailAccount) (int, error) {
	c, err := m.connect(ctx, a)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	sel, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return 0, fmt.Errorf("no se puede abrir la bandeja de entrada: %w", err)
	}
	if int64(sel.UIDValidity) != a.UIDValidity {
		// Los UID ya no valen: se empieza de cero.
		if a.UIDValidity != 0 {
			m.store.db.Exec(ctx, `DELETE FROM messages WHERE account_id = $1`, a.ID)
		}
		a.UIDValidity, a.LastUID = int64(sel.UIDValidity), 0
	}
	since := time.Now().AddDate(0, 0, -syncDays)
	recent, err := c.UIDSearch(&imap.SearchCriteria{Since: since}, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("búsqueda en el buzón: %w", err)
	}
	window := recent.AllUIDs()
	var fresh []imap.UID
	for _, u := range window {
		if int64(u) > a.LastUID {
			fresh = append(fresh, u)
		}
	}
	if a.LastUID > 0 && sel.NumMessages > 0 {
		// Lo llegado con fecha antigua (reenvíos, filtros) tampoco se pierde.
		var more imap.UIDSet
		more.AddRange(imap.UID(a.LastUID+1), 0)
		if extra, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{more}}, nil).Wait(); err == nil {
			for _, u := range extra.AllUIDs() {
				if int64(u) > a.LastUID && !slices.Contains(fresh, u) {
					fresh = append(fresh, u)
				}
			}
		}
	}
	slices.Sort(fresh)
	if a.LastUID == 0 && len(fresh) > maxFirst {
		fresh = fresh[len(fresh)-maxFirst:]
	}
	saved := 0
	for i := 0; i < len(fresh); i += 25 {
		batch := fresh[i:min(i+25, len(fresh))]
		n, err := m.fetchBatch(ctx, c, a, batch)
		saved += n
		if err != nil {
			return saved, err
		}
		a.LastUID = max(a.LastUID, int64(batch[len(batch)-1]))
	}
	if len(window) > 0 {
		if err := m.refreshFlags(ctx, c, a, window); err != nil {
			m.log.Warn("correo: leídos", "cuenta", a.Address, "err", err)
		}
	}
	if a.Drafts == "" {
		a.Drafts = findDrafts(c)
	}
	m.store.db.Exec(ctx, `DELETE FROM messages WHERE account_id = $1 AND sent_at < $2`, a.ID, time.Now().AddDate(0, 0, -keepDays))
	c.Logout().Wait()
	return saved, nil
}

func (m *mailer) fetchBatch(ctx context.Context, c *imapclient.Client, a *mailAccount, uids []imap.UID) (int, error) {
	heads, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, Flags: true, Envelope: true, RFC822Size: true, InternalDate: true}).Collect()
	if err != nil {
		return 0, fmt.Errorf("leer correos: %w", err)
	}
	var small, big []imap.UID
	info := map[imap.UID]*imapclient.FetchMessageBuffer{}
	for _, h := range heads {
		info[h.UID] = h
		if h.RFC822Size > maxRawBytes {
			big = append(big, h.UID)
		} else {
			small = append(small, h.UID)
		}
	}
	bodies := map[imap.UID][]byte{}
	for _, set := range []struct {
		uids []imap.UID
		sec  *imap.FetchItemBodySection
	}{{small, &imap.FetchItemBodySection{Peek: true}}, {big, &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true}}} {
		if len(set.uids) == 0 {
			continue
		}
		msgs, err := c.Fetch(imap.UIDSetNum(set.uids...), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{set.sec}}).Collect()
		if err != nil {
			return 0, fmt.Errorf("leer correos: %w", err)
		}
		for _, msg := range msgs {
			if len(msg.BodySection) > 0 {
				bodies[msg.UID] = msg.BodySection[0].Bytes
			}
		}
	}
	p, err := m.store.prefs(ctx, a.Owner)
	if err != nil {
		return 0, err
	}
	saved := 0
	for _, u := range uids {
		h, ok := info[u]
		if !ok || ctx.Err() != nil {
			continue
		}
		msg := fromIMAP(a.ID, h, bodies[u])
		classify(&msg, p)
		if h.RFC822Size > maxRawBytes {
			msg.Body = strings.TrimSpace(msg.Body + "\n\n(Correo de " + fmt.Sprint(h.RFC822Size>>20) + " MB: el texto no se ha descargado; ábrelo en tu correo.)")
		}
		if err := m.store.saveMessage(ctx, msg); err != nil {
			return saved, err
		}
		saved++
	}
	return saved, nil
}

func addrList(l []imap.Address) string {
	var out []string
	for _, a := range l {
		if a.Name != "" {
			out = append(out, a.Name+" <"+a.Addr()+">")
		} else if a.Addr() != "" {
			out = append(out, a.Addr())
		}
	}
	return strings.Join(out, ", ")
}

func fromIMAP(account int64, h *imapclient.FetchMessageBuffer, raw []byte) message {
	msg := message{AccountID: account, UID: int64(h.UID), Date: h.InternalDate}
	if e := h.Envelope; e != nil {
		msg.Subject, msg.MessageID = e.Subject, strings.Trim(e.MessageID, "<>")
		if !e.Date.IsZero() {
			msg.Date = e.Date
		}
		if len(e.From) > 0 {
			msg.FromName, msg.FromAddr = e.From[0].Name, strings.ToLower(e.From[0].Addr())
		}
		msg.To, msg.Cc = addrList(e.To), addrList(e.Cc)
	}
	for _, f := range h.Flags {
		switch f {
		case imap.FlagSeen:
			msg.Seen = true
		case imap.FlagFlagged:
			msg.Flagged = true
		}
	}
	msg.Signals = headerSignals(raw, msg.FromAddr)
	if len(raw) > 0 {
		body, attachments, refs := parseBody(raw)
		msg.Body, msg.Attachments, msg.Refs = body, attachments, refs
	}
	if msg.Attachments == nil {
		msg.Attachments = []string{}
	}
	if msg.Date.IsZero() {
		msg.Date = time.Now()
	}
	for _, f := range []*string{&msg.Subject, &msg.FromName, &msg.FromAddr, &msg.To, &msg.Cc, &msg.Body, &msg.Refs, &msg.MessageID} {
		*f = validText(*f)
	}
	for i := range msg.Attachments {
		msg.Attachments[i] = validText(msg.Attachments[i])
	}
	return msg
}

// validText arregla el texto que no es UTF-8: cabeceras con 8 bits sin
// declarar (un «Ñ» en ISO-8859-1, típico de programas antiguos). Se lee como
// Latin-1, que es lo habitual; así no sale «�».
func validText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	r := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && size == 1 {
			c = rune(s[i]) // byte suelto: Latin-1
		}
		r = append(r, c)
		i += size
	}
	return string(r)
}

// parseBody saca el texto (el text/plain o, si no hay, el HTML pasado a
// texto), los nombres de los adjuntos y las referencias del hilo.
func parseBody(raw []byte) (string, []string, string) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return "", nil, ""
	}
	refs := strings.Join(strings.Fields(r.Header.Get("References")), " ")
	var plain, htmlText string
	var attachments []string
	for {
		p, err := r.NextPart()
		if err != nil {
			break
		}
		switch h := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := h.ContentType()
			b, _ := io.ReadAll(io.LimitReader(p.Body, 2*maxBody))
			switch {
			case ct == "text/plain" && plain == "":
				plain = string(b)
			case ct == "text/html" && htmlText == "":
				htmlText = htmlToText(string(b))
			}
		case *mail.AttachmentHeader:
			if name, _ := h.Filename(); name != "" {
				attachments = append(attachments, name)
			}
		}
	}
	text := plain
	if strings.TrimSpace(text) == "" {
		text = htmlText
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = blankLines.ReplaceAllString(strings.TrimSpace(text), "\n\n")
	if r := []rune(text); len(text) > maxBody {
		text = string(r[:min(len(r), maxBody/2)]) + "\n…"
	}
	return text, attachments, refs
}

var (
	htmlDrop   = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	htmlBreak  = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/tr|/h[1-6])[^>]*>`)
	htmlTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	spaces     = regexp.MustCompile(`[ \t\x{a0}]+`)
	blankLines = regexp.MustCompile(`\n\s*\n+`)
)

func htmlToText(s string) string {
	s = htmlDrop.ReplaceAllString(s, "")
	s = htmlBreak.ReplaceAllString(s, "\n")
	s = htmlTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaces.ReplaceAllString(l, " "))
	}
	return strings.Join(lines, "\n")
}

// refreshFlags actualiza leídos y destacados de los últimos días y quita los
// que ya no están en la bandeja (borrados o movidos).
func (m *mailer) refreshFlags(ctx context.Context, c *imapclient.Client, a *mailAccount, window []imap.UID) error {
	msgs, err := c.Fetch(imap.UIDSetNum(window...), &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return err
	}
	present := map[int64]bool{}
	batch := &pgx.Batch{}
	for _, msg := range msgs {
		present[int64(msg.UID)] = true
		seen, flagged := slices.Contains(msg.Flags, imap.FlagSeen), slices.Contains(msg.Flags, imap.FlagFlagged)
		batch.Queue(`UPDATE messages SET seen = $3, flagged = $4 WHERE account_id = $1 AND uid = $2 AND (seen <> $3 OR flagged <> $4)`,
			a.ID, int64(msg.UID), seen, flagged)
	}
	if err := m.store.db.SendBatch(ctx, batch).Close(); err != nil {
		return err
	}
	lo := int64(slices.Min(window))
	rows, _ := m.store.db.Query(ctx, `SELECT uid FROM messages WHERE account_id = $1 AND uid >= $2`, a.ID, lo)
	local, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	var gone []int64
	for _, u := range local {
		if !present[u] && u <= a.LastUID {
			gone = append(gone, u)
		}
	}
	if len(gone) > 0 {
		_, err = m.store.db.Exec(ctx, `DELETE FROM messages WHERE account_id = $1 AND uid = ANY($2)`, a.ID, gone)
	}
	return err
}

// findDrafts busca la carpeta de borradores (por su marca o por su nombre).
func findDrafts(c *imapclient.Client) string {
	list, err := c.List("", "*", nil).Collect()
	if err != nil {
		return ""
	}
	for _, l := range list {
		if slices.Contains(l.Attrs, imap.MailboxAttrDrafts) {
			return l.Mailbox
		}
	}
	for _, name := range []string{"Drafts", "Borradores", "[Gmail]/Drafts", "[Gmail]/Borradores", "INBOX.Drafts"} {
		for _, l := range list {
			if strings.EqualFold(l.Mailbox, name) {
				return l.Mailbox
			}
		}
	}
	return ""
}

// draftInput es un borrador: se guarda en la carpeta Borradores del buzón,
// para revisarlo y enviarlo desde el correo.
type draftInput struct {
	FromName string
	To, Cc   string
	Subject  string
	Body     string
	ReplyTo  *message // respuesta a este correo (hilo y «Re:»)
}

func composeDraft(a mailAccount, in draftInput, now time.Time) ([]byte, error) {
	var h mail.Header
	h.SetDate(now)
	h.SetAddressList("From", []*mail.Address{{Name: in.FromName, Address: a.Address}})
	for _, f := range []struct{ name, value string }{{"To", in.To}, {"Cc", in.Cc}} {
		if strings.TrimSpace(f.value) == "" {
			continue
		}
		list, err := mail.ParseAddressList(f.value)
		if err != nil {
			return nil, fmt.Errorf("dirección no válida en %s: %q", map[string]string{"To": "para", "Cc": "cc"}[f.name], f.value)
		}
		h.SetAddressList(f.name, list)
	}
	subject := strings.TrimSpace(in.Subject)
	if in.ReplyTo != nil {
		if subject == "" {
			subject = in.ReplyTo.Subject
			if low := strings.ToLower(subject); !strings.HasPrefix(low, "re:") {
				subject = "Re: " + subject
			}
		}
		if id := in.ReplyTo.MessageID; id != "" {
			h.Set("In-Reply-To", "<"+id+">")
			h.Set("References", strings.TrimSpace(in.ReplyTo.Refs+" <"+id+">"))
		}
	}
	h.SetSubject(subject)
	if err := h.GenerateMessageID(); err != nil {
		return nil, err
	}
	h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	var buf bytes.Buffer
	w, err := mail.CreateSingleInlineWriter(&buf, h)
	if err != nil {
		return nil, err
	}
	io.WriteString(w, strings.ReplaceAll(in.Body, "\r\n", "\n"))
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// saveDraft deja el borrador en el buzón y devuelve la carpeta.
func (m *mailer) saveDraft(ctx context.Context, a mailAccount, in draftInput) (string, error) {
	raw, err := composeDraft(a, in, time.Now())
	if err != nil {
		return "", err
	}
	defer m.lock(a.ID)()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	c, err := m.connect(ctx, &a)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if a.Drafts == "" {
		a.Drafts = findDrafts(c)
	}
	if a.Drafts == "" {
		return "", errors.New("no encuentro la carpeta de borradores de " + a.Address)
	}
	cmd := c.Append(a.Drafts, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft, imap.FlagSeen}, Time: time.Now()})
	if _, err := cmd.Write(raw); err != nil {
		return "", err
	}
	if err := cmd.Close(); err != nil {
		return "", err
	}
	if _, err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("el servidor no acepta el borrador: %w", err)
	}
	m.store.db.Exec(ctx, `UPDATE mail_accounts SET drafts = $2, secret = $3 WHERE id = $1`, a.ID, a.Drafts, a.Secret)
	c.Logout().Wait()
	return a.Drafts, nil
}

// testLogin comprueba una cuenta nueva antes de guardarla.
func (m *mailer) testLogin(ctx context.Context, a mailAccount) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	c, err := m.connect(ctx, &a)
	if err != nil {
		return err
	}
	c.Logout().Wait()
	c.Close()
	return nil
}

// ---- Outlook: OAuth de Microsoft ----

const msScope = "offline_access openid email https://outlook.office.com/IMAP.AccessAsUser.All"

// msToken pide un token a Microsoft; devuelve el de acceso y el refresh token.
func (m *mailer) msToken(ctx context.Context, client string, form url.Values) (string, string, error) {
	if client == "" {
		return "", "", errors.New("falta el ID de la app de Microsoft (Ajustes)")
	}
	form.Set("client_id", client)
	form.Set("scope", msScope)
	tok, err := m.msPost(ctx, "/oauth2/v2.0/token", form)
	if err != nil {
		return "", "", err
	}
	return tok.AccessToken, tok.RefreshToken, nil
}

type msResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	DeviceCode   string `json:"device_code"`
	UserCode     string `json:"user_code"`
	VerifyURI    string `json:"verification_uri"`
	ExpiresIn    int    `json:"expires_in"`
	Interval     int    `json:"interval"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (m *mailer) msPost(ctx context.Context, path string, form url.Values) (msResponse, error) {
	var out msResponse
	req, _ := http.NewRequestWithContext(ctx, "POST", m.msAuth+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("Microsoft no responde: %w", err)
	}
	defer resp.Body.Close()
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if out.Error != "" {
		desc, _, _ := strings.Cut(out.Description, "\r\n")
		return out, &msError{Code: out.Error, Text: desc}
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("Microsoft respondió %s", resp.Status)
	}
	return out, nil
}

type msError struct{ Code, Text string }

func (e *msError) Error() string { return "Microsoft: " + e.Code + ": " + e.Text }

// deviceFlow es una conexión de Outlook en curso: la persona abre la página
// de Microsoft, escribe el código y autoriza; mientras, aquí se espera.
type deviceFlow struct {
	mu       sync.Mutex
	UserCode string
	URI      string
	Expires  time.Time
	Done     bool
	Address  string
	Err      string
}

func (f *deviceFlow) snapshot() deviceFlow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return deviceFlow{UserCode: f.UserCode, URI: f.URI, Expires: f.Expires, Done: f.Done, Address: f.Address, Err: f.Err}
}

func (m *mailer) flow(user string) (deviceFlow, bool) {
	v, ok := m.flows.Load(user)
	if !ok {
		return deviceFlow{}, false
	}
	return v.(*deviceFlow).snapshot(), true
}

// startOutlook empieza la conexión de una cuenta de Outlook.
func (m *mailer) startOutlook(ctx context.Context, user, client string) error {
	if client == "" {
		return errors.New("primero pon el ID de la app de Microsoft")
	}
	r, err := m.msPost(ctx, "/oauth2/v2.0/devicecode", url.Values{"client_id": {client}, "scope": {msScope}})
	if err != nil {
		return err
	}
	f := &deviceFlow{UserCode: r.UserCode, URI: r.VerifyURI, Expires: time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)}
	m.flows.Store(user, f)
	interval := time.Duration(max(r.Interval, 5)) * time.Second
	go m.waitOutlook(user, client, r.DeviceCode, f, interval)
	return nil
}

func (m *mailer) waitOutlook(user, client, device string, f *deviceFlow, interval time.Duration) {
	fail := func(msg string) {
		f.mu.Lock()
		f.Err = msg
		f.mu.Unlock()
	}
	ctx, cancel := context.WithDeadline(context.Background(), f.Expires)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			fail("El código ha caducado: vuelve a empezar.")
			return
		case <-time.After(interval):
		}
		tok, err := m.msPost(ctx, "/oauth2/v2.0/token", url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id": {client}, "device_code": {device}})
		var me *msError
		if errors.As(err, &me) && me.Code == "authorization_pending" {
			continue
		}
		if errors.As(err, &me) && me.Code == "slow_down" {
			interval += 5 * time.Second
			continue
		}
		if err != nil {
			fail(err.Error())
			return
		}
		address := idTokenEmail(tok.IDToken)
		if address == "" || tok.RefreshToken == "" {
			fail("Microsoft no ha devuelto la dirección o el permiso permanente: revisa los permisos de la app.")
			return
		}
		a := mailAccount{Owner: user, Kind: "outlook", Address: address, Host: "outlook.office365.com", Port: 993,
			Username: address, Secret: m.seal.seal(tok.RefreshToken), OAuthClient: client}
		if err := m.testLogin(ctx, a); err != nil {
			fail(err.Error())
			return
		}
		id, err := m.store.addAccount(context.Background(), a)
		if err != nil {
			fail(err.Error())
			return
		}
		f.mu.Lock()
		f.Done, f.Address = true, address
		f.mu.Unlock()
		a.ID = id
		go m.syncAccount(context.Background(), a)
		return
	}
}

// idTokenEmail lee la dirección del id_token (llega directo de Microsoft por
// TLS, así que no hace falta comprobar su firma).
func idTokenEmail(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
	}
	json.Unmarshal(b, &claims)
	for _, v := range []string{claims.Email, claims.PreferredUsername} {
		if strings.Contains(v, "@") {
			return strings.ToLower(v)
		}
	}
	return ""
}

// presets de las cuentas que se conectan con contraseña de aplicación.
var imapPresets = map[string]struct {
	Host string
	Port int
}{
	"gmail": {"imap.gmail.com", 993},
}
