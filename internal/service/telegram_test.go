package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"communications-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testTelegramToken = "123456:test-only-token"
const testTelegramChat = "-100987654321"

type telegramRoundTripper func(*http.Request) (*http.Response, error)

func (f telegramRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func telegramTestService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := upstream.Client()
	transport := client.Transport
	client.Transport = telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || r.URL.Path != "/bot"+testTelegramToken+"/sendMessage" {
			t.Error("incorrect Telegram endpoint")
		}
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = u.Scheme, u.Host
		return transport.RoundTrip(copy)
	})
	return &Service{cfg: config.Config{TelegramBotToken: testTelegramToken, TelegramChatID: testTelegramChat}, httpClient: client}
}

func TestTelegramSendsPlainText(t *testing.T) {
	message := " Alert: A_B * [x] <tag> & $SPY\nEspañol: mañana 🚀 "
	s := telegramTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected JSON POST")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["chat_id"] != testTelegramChat || payload["text"] != message || len(payload) != 2 {
			t.Errorf("unexpected payload: %v", payload)
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
	})
	_, result, err := s.telegramNotify(context.Background(), nil, TelegramNotifyInput{Message: message})
	if err != nil || result.MessageID != 42 || result.Status != "sent" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestTelegramValidation(t *testing.T) {
	for _, tt := range []struct{ name, token, chat, message, want string }{
		{"missing token", "", testTelegramChat, "hi", "not configured"},
		{"missing chat", testTelegramToken, "", "hi", "not configured"},
		{"bad token", "bad/token?query", testTelegramChat, "hi", "invalid format"},
		{"empty message", testTelegramToken, testTelegramChat, " \n\t", "message is required"},
		{"over limit", testTelegramToken, testTelegramChat, strings.Repeat("é", 4097), "4096"},
		{"invalid UTF8", testTelegramToken, testTelegramChat, string([]byte{0xff}), "UTF-8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{cfg: config.Config{TelegramBotToken: tt.token, TelegramChatID: tt.chat}}
			_, _, err := s.telegramNotify(context.Background(), nil, TelegramNotifyInput{Message: tt.message})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	s := telegramTestService(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	})
	_, _, err := s.telegramNotify(context.Background(), nil, TelegramNotifyInput{Message: strings.Repeat("é", 4096)})
	if err != nil {
		t.Fatalf("4096 Unicode characters rejected: %v", err)
	}
}

func TestTelegramErrorsAndRetryLimits(t *testing.T) {
	for _, tt := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"unauthorized", 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, "401: Unauthorized"},
		{"blocked", 403, `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`, "403: bot was blocked"},
		{"bad chat", 400, `{"ok":false,"error_code":400,"description":"chat not found"}`, "chat not found"},
		{"API failure on HTTP 200", 200, `{"ok":false,"error_code":400,"description":"bad request"}`, "400: bad request"},
		{"server error", 500, `{"ok":false,"description":"Internal error"}`, "500"},
		{"long wait", 429, `{"ok":false,"parameters":{"retry_after":60}}`, "retry_after=60"},
		{"overflow wait", 429, `{"ok":false,"parameters":{"retry_after":9223372036854775807}}`, "retry_after=9223372036854775807"},
		{"missing retry_after", 429, `{"ok":false}`, "retry_after was not provided"},
		{"invalid JSON", 502, "not JSON: " + testTelegramToken, "invalid response"},
		{"oversized response", 200, strings.Repeat("x", maxTelegramResponseBytes+1), "invalid response"},
		{"missing message ID", 200, `{"ok":true,"result":{}}`, "no message_id"},
		{"redacted description", 400, fmt.Sprintf(`{"ok":false,"description":"%s %s"}`, testTelegramToken, testTelegramChat), "[redacted]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			s := telegramTestService(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			})
			_, _, err := s.telegramNotify(context.Background(), nil, TelegramNotifyInput{Message: "hi"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), testTelegramToken) || strings.Contains(err.Error(), testTelegramChat) {
				t.Fatal("credential leaked")
			}
			if calls != 1 {
				t.Fatalf("unexpected retry; calls=%d", calls)
			}
		})
	}
}

func TestTelegramRateLimitRetry(t *testing.T) {
	for _, secondStatus := range []int{200, 429} {
		t.Run(fmt.Sprint(secondStatus), func(t *testing.T) {
			calls := 0
			var first time.Time
			s := telegramTestService(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					first = time.Now()
				} else if time.Since(first) < time.Second {
					t.Error("retry_after not respected")
				}
				if calls == 1 || secondStatus == 429 {
					w.WriteHeader(429)
					io.WriteString(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":1}}`)
					return
				}
				io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
			})
			_, result, err := s.telegramNotify(context.Background(), nil, TelegramNotifyInput{Message: "hi"})
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
			if secondStatus == 200 && (err != nil || result.MessageID != 7) {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if secondStatus == 429 && (err == nil || !strings.Contains(err.Error(), "retry_after=1")) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestTelegramRateLimitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	s := &Service{cfg: config.Config{TelegramBotToken: testTelegramToken, TelegramChatID: testTelegramChat}, httpClient: &http.Client{Transport: telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"parameters":{"retry_after":30}}`)), Request: r}, nil
	})}}
	_, _, err := s.telegramNotify(ctx, nil, TelegramNotifyInput{Message: "hi"})
	if err == nil || !strings.Contains(err.Error(), "wait canceled") || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestTelegramTransportErrorRedactsToken(t *testing.T) {
	calls := 0
	s := &Service{cfg: config.Config{TelegramBotToken: testTelegramToken, TelegramChatID: testTelegramChat}, httpClient: &http.Client{Transport: telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("network error at " + r.URL.String())
	})}}
	_, _, err := s.telegramNotify(context.Background(), nil, TelegramNotifyInput{Message: "hi"})
	if err == nil || strings.Contains(err.Error(), testTelegramToken) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestTelegramDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	s := telegramTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Location", "https://example.invalid/collect")
		w.WriteHeader(http.StatusTemporaryRedirect)
		io.WriteString(w, `{"ok":false}`)
	})
	_, _, err := s.telegramNotify(context.Background(), nil, TelegramNotifyInput{Message: "hi"})
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestTelegramThroughMCP(t *testing.T) {
	s := telegramTestService(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	s.RegisterTools(server)
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer endpoint.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"telegram_notify", "send_general_sms", "send_priority_sms", "place_general_call", "place_priority_call", "send_ntfy_notification"} {
		if !names[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "telegram_notify", Arguments: map[string]any{"message": "MCP test"}})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(encoded), `"message_id":99`) {
		t.Fatalf("unexpected result: %s", encoded)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "telegram_notify", Arguments: map[string]any{"message": ""}})
	if err != nil || !result.IsError {
		t.Fatalf("expected MCP tool error, result=%+v error=%v", result, err)
	}
}
