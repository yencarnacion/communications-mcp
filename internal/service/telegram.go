package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxTelegramChars         = 4096
	maxTelegramResponseBytes = 64 << 10
	maxTelegramRetryWait     = 30 * time.Second
)

var telegramTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

type TelegramNotifyInput struct {
	Message string `json:"message" jsonschema:"Plain-text message to send to the configured Telegram chat; 1-4096 characters"`
}

type TelegramResult struct {
	MessageID int64  `json:"message_id"`
	Status    string `json:"status"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Result      struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
	Parameters struct {
		RetryAfter int64 `json:"retry_after"`
	} `json:"parameters"`
}

func (s *Service) telegramNotify(ctx context.Context, _ *mcp.CallToolRequest, input TelegramNotifyInput) (*mcp.CallToolResult, TelegramResult, error) {
	token := strings.TrimSpace(s.cfg.TelegramBotToken)
	chatID := strings.TrimSpace(s.cfg.TelegramChatID)
	if token == "" || chatID == "" {
		return nil, TelegramResult{}, errors.New("Telegram is not configured: set both TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID (or both DSH_TELEGRAM_BOT_TOKEN and DSH_TELEGRAM_CHAT_ID) in the environment or .env")
	}
	if !telegramTokenPattern.MatchString(token) {
		return nil, TelegramResult{}, errors.New("Telegram bot token has an invalid format; check TELEGRAM_BOT_TOKEN or DSH_TELEGRAM_BOT_TOKEN")
	}
	if strings.TrimSpace(input.Message) == "" {
		return nil, TelegramResult{}, errors.New("message is required")
	}
	if !utf8.ValidString(input.Message) || utf8.RuneCountInString(input.Message) > maxTelegramChars {
		return nil, TelegramResult{}, errors.New("message must be valid UTF-8 and at most 4096 characters; split longer messages before sending")
	}

	// Do not enable parse_mode: plain text preserves punctuation, underscores,
	// and arbitrary notifications without requiring Markdown/HTML escaping.
	payload, err := json.Marshal(struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}{ChatID: chatID, Text: input.Message})
	if err != nil {
		return nil, TelegramResult{}, errors.New("failed to encode Telegram message")
	}

	// Telegram embeds its credential in the URL. Never follow redirects or
	// return raw HTTP errors (url.Error includes that URL and therefore token).
	client := *s.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Timeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	for attempt := 0; attempt < 2; attempt++ {
		data, status, err := sendTelegramRequest(ctx, &client, token, payload)
		if err != nil {
			return nil, TelegramResult{}, err
		}
		if status >= 200 && status < 300 && data.OK {
			if data.Result.MessageID <= 0 {
				return nil, TelegramResult{}, errors.New("Telegram returned no message_id; delivery status is unknown, check the chat before retrying")
			}
			return nil, TelegramResult{MessageID: data.Result.MessageID, Status: "sent"}, nil
		}

		if status == http.StatusTooManyRequests || data.ErrorCode == http.StatusTooManyRequests {
			retryAfter := data.Parameters.RetryAfter
			// Retry once, only after an explicit flood-control rejection. Do not
			// retry timeouts/5xx: Telegram may already have delivered the message.
			if attempt == 0 && retryAfter > 0 && retryAfter <= int64(maxTelegramRetryWait/time.Second) {
				timer := time.NewTimer(time.Duration(retryAfter) * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, TelegramResult{}, errors.New("Telegram rate-limit wait canceled; message was not retried")
				case <-timer.C:
					continue
				}
			}
			if retryAfter > 0 {
				return nil, TelegramResult{}, fmt.Errorf("Telegram rate limit: retry_after=%d seconds; wait at least that long before sending again", retryAfter)
			}
			return nil, TelegramResult{}, errors.New("Telegram rate limit (429); retry_after was not provided, try again later")
		}

		code := data.ErrorCode
		if code == 0 {
			code = status
		}
		// Redact secrets even if an upstream response echoes the request.
		description := strings.NewReplacer(token, "[redacted]", chatID, "[redacted]").Replace(data.Description)
		description = sanitizeLine(description, 500)
		if description == "" {
			description = "request rejected"
		}
		return nil, TelegramResult{}, fmt.Errorf("Telegram API error %d: %s", code, description)
	}
	return nil, TelegramResult{}, errors.New("Telegram retry limit reached")
}

func sendTelegramRequest(ctx context.Context, client *http.Client, token string, payload []byte) (telegramResponse, int, error) {
	var data telegramResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return data, 0, errors.New("failed to create Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return data, 0, errors.New("Telegram request failed or timed out; delivery status is unknown, check the chat before retrying")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTelegramResponseBytes+1))
	if err != nil {
		return data, resp.StatusCode, errors.New("failed to read Telegram response; delivery status is unknown, check the chat before retrying")
	}
	if len(body) > maxTelegramResponseBytes || json.Unmarshal(body, &data) != nil {
		// Never include the raw body; proxies and servers may echo credentials.
		return data, resp.StatusCode, fmt.Errorf("Telegram returned an invalid response (HTTP %d); check the chat before retrying", resp.StatusCode)
	}
	return data, resp.StatusCode, nil
}
