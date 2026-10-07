package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Alerts by Telegram ([alerts] telegram_*; user): a bot of the admin sends them to the
// admin's chat. The bot's token is read from a file only the admin can read, never from
// server.conf.

var telegramAPI = "https://api.telegram.org" // a variable for the tests

// secretFile reads a one-line secret from a file that other users cannot read.
func secretFile(path, what string) (string, error) {
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		return "", fmt.Errorf("%s (%s) can be read by other users: chmod 600 %s", path, what, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %v", what, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func sendTelegram(cfg *Config, text string) error {
	token, err := secretFile(cfg.TelegramTokenFile, "Telegram bot token file")
	if err != nil {
		return err
	}
	if !botToken.MatchString(token) { // nothing else may reach the URL (or an error message)
		return fmt.Errorf("%s does not hold a bot token (only the token, e.g. 123456:ABC-def..., as @BotFather gave it)", cfg.TelegramTokenFile)
	}
	if r := []rune(text); len(r) > 4000 { // Telegram's limit is 4096 characters (not bytes)
		text = string(r[:4000]) + "\n..."
	}
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.PostForm(telegramAPI+"/bot"+token+"/sendMessage", url.Values{"chat_id": {cfg.TelegramChat}, "text": {text}})
	if err != nil {
		// the error text contains the URL with the token (sometimes quoted or escaped, so it
		// cannot be masked reliably): never show it
		return fmt.Errorf("cannot reach Telegram (network error or timeout)")
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	if !r.OK {
		hint := ""
		if strings.Contains(r.Description, "chat not found") || strings.Contains(r.Description, "bot can't initiate") {
			hint = " (send /start to the bot from your Telegram first, and check telegram_chat)"
		} else if resp.StatusCode == http.StatusUnauthorized {
			hint = " (the token in the file is wrong)"
		}
		return fmt.Errorf("Telegram refused the message: %s%s", r.Description, hint)
	}
	return nil
}

var (
	appPassword = regexp.MustCompile(`^[a-zA-Z]{4}( [a-zA-Z]{4}){3}$`)
	botToken    = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{20,80}$`)
)
