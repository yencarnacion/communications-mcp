package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTelegramFromDotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "DSH_TELEGRAM_BOT_TOKEN", "DSH_TELEGRAM_CHAT_ID"} {
		t.Setenv(key, "") // Register cleanup before unsetting for godotenv.Load.
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"TWILIO_ACCOUNT_SID": "test", "TWILIO_AUTH_TOKEN": "test", "OPENAI_API_KEY": "test",
		"MCP_PORT": "8084", "NTFY_TOPIC": "test", "GCS_BUCKET_NAME": "test",
	} {
		t.Setenv(key, value)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TELEGRAM_BOT_TOKEN=123:dotenv-token\nTELEGRAM_CHAT_ID=-100123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramBotToken != "123:dotenv-token" || cfg.TelegramChatID != "-100123" {
		t.Fatal(".env credentials were not loaded")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "456:exported-token")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramBotToken != "456:exported-token" {
		t.Fatal(".env replaced exported value")
	}
}

func TestLoadTelegramCredentials(t *testing.T) {
	for _, tt := range []struct {
		name                           string
		token, chat, dshToken, dshChat string
		wantToken, wantChat            string
	}{
		{name: "optional"},
		{name: "canonical", token: " 123:test-token ", chat: " -100123 ", wantToken: "123:test-token", wantChat: "-100123"},
		{name: "DSH aliases", dshToken: " 456:alias-token ", dshChat: " 987 ", wantToken: "456:alias-token", wantChat: "987"},
		{name: "canonical wins", token: "123:test-token", chat: "321", dshToken: "456:alias-token", dshChat: "987", wantToken: "123:test-token", wantChat: "321"},
		{name: "no mixed pairs", token: "123:test-token", dshToken: "456:alias-token", dshChat: "987", wantToken: "123:test-token"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for key, value := range map[string]string{
				"TWILIO_ACCOUNT_SID": "test", "TWILIO_AUTH_TOKEN": "test", "OPENAI_API_KEY": "test",
				"MCP_PORT": "8084", "NTFY_TOPIC": "test", "GCS_BUCKET_NAME": "test",
				"TELEGRAM_BOT_TOKEN": tt.token, "TELEGRAM_CHAT_ID": tt.chat,
				"DSH_TELEGRAM_BOT_TOKEN": tt.dshToken, "DSH_TELEGRAM_CHAT_ID": tt.dshChat,
			} {
				t.Setenv(key, value)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TelegramBotToken != tt.wantToken || cfg.TelegramChatID != tt.wantChat {
				t.Fatal("unexpected Telegram credential selection")
			}
		})
	}
}

func TestLoadStillRequiresExistingProviders(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, key := range []string{"TWILIO_ACCOUNT_SID", "TWILIO_AUTH_TOKEN", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("MCP_PORT", "8084")
	t.Setenv("NTFY_TOPIC", "test")
	t.Setenv("GCS_BUCKET_NAME", "test")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "missing required environment variables") {
		t.Fatalf("unexpected error: %v", err)
	}
}
