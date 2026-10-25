package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const (
	defaultBindAddress     = "0.0.0.0"
	defaultPort            = 8084
	defaultTTSModel        = "tts-1-hd"
	defaultTTSVoice        = "nova"
	defaultGCSBucket       = "przone-dojo-tmp-assets"
	defaultGCSPublicBase   = "https://storage.googleapis.com"
	defaultGCSObjectPrefix = "communications-mcp"
	defaultNTFYBaseURL     = "http://push.webgazer.org"
	defaultNTFYTopic       = "rain"
)

type Config struct {
	BindAddress string
	Port        int

	TwilioAccountSID string
	TwilioAuthToken  string
	GeneralFrom      string
	PriorityFrom     string
	PersonalTo       string

	OpenAIAPIKey   string
	OpenAITTSModel string
	OpenAITTSVoice string

	GCSBucketName    string
	GCSPublicBaseURL string
	GCSObjectPrefix  string
	GCSSkipObjectACL bool

	NTFYBaseURL  string
	NTFYTopic    string
	NTFYUsername string
	NTFYPassword string

	TelegramBotToken string
	TelegramChatID   string
}

func Load() (Config, error) {
	_ = godotenv.Load()
	telegramToken, telegramChatID := telegramCredentials()

	cfg := Config{
		BindAddress: strings.TrimSpace(getEnv("MCP_BIND_ADDRESS", defaultBindAddress)),
		Port:        getIntEnv("MCP_PORT", defaultPort),

		TwilioAccountSID: strings.TrimSpace(os.Getenv("TWILIO_ACCOUNT_SID")),
		TwilioAuthToken:  strings.TrimSpace(os.Getenv("TWILIO_AUTH_TOKEN")),
		GeneralFrom:      strings.TrimSpace(getEnv("TWILIO_PHONE_GENERAL", "+14782474908")),
		PriorityFrom:     strings.TrimSpace(getEnv("TWILIO_PHONE_PRIORITY", "+19375000711")),
		PersonalTo:       strings.TrimSpace(getEnv("PERSONAL_PHONE_NUMBER", "+17874387562")),

		OpenAIAPIKey:   strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		OpenAITTSModel: strings.TrimSpace(getEnv("OPENAI_TTS_MODEL", defaultTTSModel)),
		OpenAITTSVoice: strings.TrimSpace(getEnv("OPENAI_TTS_VOICE", defaultTTSVoice)),

		GCSBucketName:    strings.TrimSpace(getEnv("GCS_BUCKET_NAME", defaultGCSBucket)),
		GCSPublicBaseURL: strings.TrimSpace(getEnv("GCS_PUBLIC_BASE_URL", defaultGCSPublicBase)),
		GCSObjectPrefix:  strings.Trim(strings.TrimSpace(getEnv("GCS_OBJECT_PREFIX", defaultGCSObjectPrefix)), "/"),
		GCSSkipObjectACL: getBoolEnv("GCS_SKIP_OBJECT_ACL", false),

		NTFYBaseURL:  strings.TrimRight(strings.TrimSpace(getEnv("NTFY_BASE_URL", defaultNTFYBaseURL)), "/"),
		NTFYTopic:    strings.TrimSpace(getEnv("NTFY_TOPIC", defaultNTFYTopic)),
		NTFYUsername: strings.TrimSpace(os.Getenv("NTFY_USERNAME")),
		NTFYPassword: strings.TrimSpace(os.Getenv("NTFY_PASSWORD")),

		TelegramBotToken: telegramToken,
		TelegramChatID:   telegramChatID,
	}

	if cfg.BindAddress == "" {
		cfg.BindAddress = defaultBindAddress
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("MCP_PORT must be between 1 and 65535")
	}
	if cfg.NTFYTopic == "" {
		return Config{}, fmt.Errorf("NTFY_TOPIC must not be empty")
	}
	if cfg.GCSBucketName == "" {
		return Config{}, fmt.Errorf("GCS_BUCKET_NAME must not be empty")
	}

	missing := make([]string, 0, 2)
	if cfg.TwilioAccountSID == "" {
		missing = append(missing, "TWILIO_ACCOUNT_SID")
	}
	if cfg.TwilioAuthToken == "" {
		missing = append(missing, "TWILIO_AUTH_TOKEN")
	}
	if cfg.OpenAIAPIKey == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

// Prefer the TELEGRAM pair when either variable is configured; never combine
// credentials from two different bots/chats. The DSH pair is a compatibility alias.
func telegramCredentials() (string, string) {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	chatID := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	if token != "" || chatID != "" {
		return token, chatID
	}
	return strings.TrimSpace(os.Getenv("DSH_TELEGRAM_BOT_TOKEN")), strings.TrimSpace(os.Getenv("DSH_TELEGRAM_CHAT_ID"))
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getIntEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getBoolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
