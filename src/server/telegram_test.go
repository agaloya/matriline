package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTelegram(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if !strings.HasPrefix(r.URL.Path, "/bot123456:SECRETabcdefghijklmnopqrstuv/") {
			w.WriteHeader(401)
			w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
			return
		}
		if r.Form.Get("chat_id") != "42" {
			w.WriteHeader(400)
			w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
			return
		}
		got = r.Form.Get("text")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	old := telegramAPI
	telegramAPI = srv.URL
	defer func() { telegramAPI = old }()
	tok := filepath.Join(t.TempDir(), "tok")
	os.WriteFile(tok, []byte("123456:SECRETabcdefghijklmnopqrstuv\n"), 0o600)
	cfg := &Config{TelegramEnabled: true, TelegramTokenFile: tok, TelegramChat: "42"}
	if err := sendTelegram(cfg, "[matriline] test\nhello"); err != nil || got != "[matriline] test\nhello" {
		t.Fatalf("send: %v, got %q", err, got)
	}
	cfg.TelegramChat = "7"
	if err := sendTelegram(cfg, "x"); err == nil || !strings.Contains(err.Error(), "/start") {
		t.Errorf("unknown chat: %v", err)
	}
	if runtime.GOOS != "windows" {
		os.Chmod(tok, 0o644)
		if err := sendTelegram(cfg, "x"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("token file readable by others: %v", err)
		}
		os.Chmod(tok, 0o600)
	}
	telegramAPI = "http://127.0.0.1:1" // nothing listens: the error must not show the token
	if err := sendTelegram(cfg, "x"); err == nil || strings.Contains(err.Error(), "SECRETabc") {
		t.Errorf("unreachable: %v", err)
	}
	// a file in another format (token=... and chat=... lines): refused, the token never shown
	os.WriteFile(tok, []byte("token=123456:SECRETabcdefghijklmnopqrstuv\nchat=42\n"), 0o600)
	if err := sendTelegram(cfg, "x"); err == nil || strings.Contains(err.Error(), "SECRETabc") {
		t.Errorf("two-line file: %v", err)
	}
}
