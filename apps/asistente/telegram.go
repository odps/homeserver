package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Cliente mínimo de la Bot API de Telegram. Se reciben los mensajes con long
// polling: no hay que abrir puertos.

type telegram struct {
	base  string // https://api.telegram.org
	token string
	http  *http.Client
}

type tgUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type tgMessage struct {
	MessageID int64  `json:"message_id"`
	From      tgUser `json:"from"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text     string        `json:"text"`
	Location *tgLocation   `json:"location"`
	Photo    []tgPhotoSize `json:"photo"`
	Document *tgFileRef    `json:"document"`
	Voice    *tgFileRef    `json:"voice"`
	Audio    *tgFileRef    `json:"audio"`
	Caption  string        `json:"caption"`
}

type tgLocation struct {
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	LivePeriod int     `json:"live_period"` // segundos; 0 si no es en tiempo real
}

type tgUpdate struct {
	UpdateID int64       `json:"update_id"`
	Message  *tgMessage  `json:"message"`
	Edited   *tgMessage  `json:"edited_message"` // la ubicación en tiempo real llega así
	Callback *tgCallback `json:"callback_query"` // un botón pulsado
}

var errUnauthorized = errors.New("Telegram rechaza el token del bot")

func (t telegram) call(ctx context.Context, method string, params, out any) error {
	body, _ := json.Marshal(params)
	req, _ := http.NewRequestWithContext(ctx, "POST", t.base+"/bot"+t.token+"/"+method, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		// El error de net/http incluye la URL, que lleva el token: no se propaga.
		return fmt.Errorf("Telegram no responde (%s)", strings.ReplaceAll(err.Error(), t.token, "…"))
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return fmt.Errorf("respuesta de Telegram no válida (%s)", resp.Status)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		return errUnauthorized
	}
	if !r.OK {
		return fmt.Errorf("Telegram: %s", r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (t telegram) me(ctx context.Context) (tgUser, error) {
	var u tgUser
	err := t.call(ctx, "getMe", map[string]any{}, &u)
	return u, err
}

func (t telegram) updates(ctx context.Context, offset int64, wait time.Duration) ([]tgUpdate, error) {
	var u []tgUpdate
	err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": int(wait.Seconds()), "allowed_updates": []string{"message", "edited_message", "callback_query"}}, &u)
	return u, err
}

// send manda un texto con el formato HTML de Telegram, en trozos si es largo.
// Si Telegram rechaza el formato, lo manda como texto plano.
func (t telegram) send(ctx context.Context, chat int64, text string) error {
	for _, part := range splitMessage(plainText(text), 3500) {
		err := t.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": part,
			"link_preview_options": map[string]bool{"is_disabled": true}}, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func (t telegram) typing(ctx context.Context, chat int64) {
	t.call(ctx, "sendChatAction", map[string]any{"chat_id": chat, "action": "typing"}, nil)
}

// splitMessage parte un texto largo por párrafos o líneas.
func splitMessage(s string, max int) []string {
	var parts []string
	for len(s) > max {
		cut := strings.LastIndex(s[:max], "\n\n")
		if cut < max/2 {
			cut = strings.LastIndex(s[:max], "\n")
		}
		if cut < max/2 {
			cut = max
		}
		parts = append(parts, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}
