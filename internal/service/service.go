package service

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"communications-mcp/internal/config"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nyaruka/phonenumbers"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/twilio/twilio-go"
	api "github.com/twilio/twilio-go/rest/api/v2010"
)

const (
	lineGeneral  = "general"
	linePriority = "priority"
	maxSMSChars  = 1600
	maxCallChars = 1800
	maxNtfyChars = 4000
)

type Service struct {
	cfg           config.Config
	logger        *slog.Logger
	twilioClient  *twilio.RestClient
	openAIClient  openai.Client
	storageClient *storage.Client
	httpClient    *http.Client

	generalFrom  string
	priorityFrom string
	personalTo   string
}

type SendGeneralSMSInput struct {
	To      string `json:"to" jsonschema:"Recipient phone number in E.164 format or as a 10-digit US/Puerto Rico number"`
	Message string `json:"message" jsonschema:"SMS message body to send"`
}

type SendPrioritySMSInput struct {
	Message string `json:"message" jsonschema:"SMS message body to send to the configured personal phone number"`
}

type PlaceGeneralCallInput struct {
	To          string `json:"to" jsonschema:"Recipient phone number in E.164 format or as a 10-digit US/Puerto Rico number"`
	Message     string `json:"message" jsonschema:"Text that will be converted to speech and played during the call"`
	RepeatCount int    `json:"repeat_count,omitempty" jsonschema:"Optional repeat count for the spoken message. Defaults to 2"`
}

type PlacePriorityCallInput struct {
	Message     string `json:"message" jsonschema:"Text that will be converted to speech and played to the configured personal phone number"`
	RepeatCount int    `json:"repeat_count,omitempty" jsonschema:"Optional repeat count for the spoken message. Defaults to 2"`
}

type SendNTFYInput struct {
	Title    string   `json:"title" jsonschema:"Notification title"`
	Message  string   `json:"message,omitempty" jsonschema:"Optional notification body"`
	Topic    string   `json:"topic,omitempty" jsonschema:"Optional ntfy topic override. Defaults to the configured topic"`
	Tags     []string `json:"tags,omitempty" jsonschema:"Optional ntfy tags"`
	Priority int      `json:"priority,omitempty" jsonschema:"Optional ntfy priority from 1 to 5"`
	ClickURL string   `json:"click_url,omitempty" jsonschema:"Optional URL opened when the notification is clicked"`
}

type SMSResult struct {
	Line       string `json:"line"`
	From       string `json:"from"`
	To         string `json:"to"`
	MessageSID string `json:"message_sid"`
	Status     string `json:"status"`
}

type CallResult struct {
	Line       string `json:"line"`
	From       string `json:"from"`
	To         string `json:"to"`
	CallSID    string `json:"call_sid"`
	Status     string `json:"status"`
	AudioURL   string `json:"audio_url"`
	ObjectName string `json:"object_name"`
}

type NTFYResult struct {
	BaseURL    string `json:"base_url"`
	Topic      string `json:"topic"`
	StatusCode int    `json:"status_code"`
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Service, error) {
	generalFrom, err := normalizePhoneNumber(cfg.GeneralFrom)
	if err != nil {
		return nil, fmt.Errorf("invalid TWILIO_PHONE_GENERAL: %w", err)
	}
	priorityFrom, err := normalizePhoneNumber(cfg.PriorityFrom)
	if err != nil {
		return nil, fmt.Errorf("invalid TWILIO_PHONE_PRIORITY: %w", err)
	}
	personalTo, err := normalizePhoneNumber(cfg.PersonalTo)
	if err != nil {
		return nil, fmt.Errorf("invalid PERSONAL_PHONE_NUMBER: %w", err)
	}
	if err := requireUSOrPRNumber(generalFrom); err != nil {
		return nil, fmt.Errorf("TWILIO_PHONE_GENERAL must be a US or Puerto Rico number: %w", err)
	}
	if err := requireUSOrPRNumber(priorityFrom); err != nil {
		return nil, fmt.Errorf("TWILIO_PHONE_PRIORITY must be a US or Puerto Rico number: %w", err)
	}
	if err := requireUSOrPRNumber(personalTo); err != nil {
		return nil, fmt.Errorf("PERSONAL_PHONE_NUMBER must be a US or Puerto Rico number: %w", err)
	}

	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}

	twilioClient := twilio.NewRestClientWithParams(twilio.ClientParams{
		Username:   cfg.TwilioAccountSID,
		Password:   cfg.TwilioAuthToken,
		AccountSid: cfg.TwilioAccountSID,
	})

	openAIClient := openai.NewClient(
		option.WithAPIKey(cfg.OpenAIAPIKey),
		option.WithHTTPClient(&http.Client{}),
	)

	return &Service{
		cfg:           cfg,
		logger:        logger,
		twilioClient:  twilioClient,
		openAIClient:  openAIClient,
		storageClient: storageClient,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		generalFrom:  generalFrom,
		priorityFrom: priorityFrom,
		personalTo:   personalTo,
	}, nil
}

func (s *Service) Close() error {
	if s.storageClient != nil {
		return s.storageClient.Close()
	}
	return nil
}

func (s *Service) RegisterTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "telegram_notify",
		Description: "Send a plain-text Telegram message (1-4096 characters) to the configured chat only. Requires TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID, or the DSH_TELEGRAM aliases.",
	}, s.telegramNotify)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_general_sms",
		Description: "Send an SMS from the general-purpose Twilio number to a US or Puerto Rico number.",
	}, s.sendGeneralSMS)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_priority_sms",
		Description: "Send an SMS from the priority Twilio number to the configured personal phone number only.",
	}, s.sendPrioritySMS)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "place_general_call",
		Description: "Generate speech, upload it to Google Cloud Storage, and place a call from the general-purpose Twilio number to a US or Puerto Rico number.",
	}, s.placeGeneralCall)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "place_priority_call",
		Description: "Generate speech, upload it to Google Cloud Storage, and place a call from the priority Twilio number to the configured personal phone number only.",
	}, s.placePriorityCall)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_ntfy_notification",
		Description: "Send an ntfy notification to the default configured topic or to a caller-specified topic.",
	}, s.sendNTFYNotification)
}

func (s *Service) sendGeneralSMS(ctx context.Context, _ *mcp.CallToolRequest, input SendGeneralSMSInput) (*mcp.CallToolResult, SMSResult, error) {
	to, err := normalizePhoneNumber(input.To)
	if err != nil {
		return nil, SMSResult{}, err
	}
	if err := requireUSOrPRNumber(to); err != nil {
		return nil, SMSResult{}, err
	}

	return s.sendSMS(ctx, lineGeneral, s.generalFrom, to, input.Message)
}

func (s *Service) sendPrioritySMS(ctx context.Context, _ *mcp.CallToolRequest, input SendPrioritySMSInput) (*mcp.CallToolResult, SMSResult, error) {
	return s.sendSMS(ctx, linePriority, s.priorityFrom, s.personalTo, input.Message)
}

func (s *Service) placeGeneralCall(ctx context.Context, _ *mcp.CallToolRequest, input PlaceGeneralCallInput) (*mcp.CallToolResult, CallResult, error) {
	to, err := normalizePhoneNumber(input.To)
	if err != nil {
		return nil, CallResult{}, err
	}
	if err := requireUSOrPRNumber(to); err != nil {
		return nil, CallResult{}, err
	}

	return s.placeCall(ctx, lineGeneral, s.generalFrom, to, input.Message, input.RepeatCount)
}

func (s *Service) placePriorityCall(ctx context.Context, _ *mcp.CallToolRequest, input PlacePriorityCallInput) (*mcp.CallToolResult, CallResult, error) {
	return s.placeCall(ctx, linePriority, s.priorityFrom, s.personalTo, input.Message, input.RepeatCount)
}

func (s *Service) sendNTFYNotification(ctx context.Context, _ *mcp.CallToolRequest, input SendNTFYInput) (*mcp.CallToolResult, NTFYResult, error) {
	title := sanitizeLine(input.Title, 200)
	if title == "" {
		return nil, NTFYResult{}, errors.New("title is required")
	}

	topic := sanitizeTopic(input.Topic)
	if topic == "" {
		topic = s.cfg.NTFYTopic
	}

	body := sanitizeMultiline(input.Message, maxNtfyChars)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.NTFYBaseURL+"/"+topic, strings.NewReader(body))
	if err != nil {
		return nil, NTFYResult{}, fmt.Errorf("failed to create ntfy request: %w", err)
	}

	req.Header.Set("Title", title)
	if len(input.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(input.Tags, ","))
	}
	if input.Priority != 0 {
		if input.Priority < 1 || input.Priority > 5 {
			return nil, NTFYResult{}, errors.New("priority must be between 1 and 5")
		}
		req.Header.Set("Priority", fmt.Sprintf("%d", input.Priority))
	}
	if clickURL := strings.TrimSpace(input.ClickURL); clickURL != "" {
		req.Header.Set("Click", clickURL)
	}
	if s.cfg.NTFYUsername != "" || s.cfg.NTFYPassword != "" {
		req.SetBasicAuth(s.cfg.NTFYUsername, s.cfg.NTFYPassword)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, NTFYResult{}, fmt.Errorf("failed to send ntfy notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, NTFYResult{}, fmt.Errorf("ntfy returned %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	return nil, NTFYResult{
		BaseURL:    s.cfg.NTFYBaseURL,
		Topic:      topic,
		StatusCode: resp.StatusCode,
	}, nil
}

func (s *Service) sendSMS(ctx context.Context, lineName, from, to, message string) (*mcp.CallToolResult, SMSResult, error) {
	body := sanitizeMultiline(message, maxSMSChars)
	if body == "" {
		return nil, SMSResult{}, errors.New("message is required")
	}

	params := &api.CreateMessageParams{}
	params.SetTo(to)
	params.SetFrom(from)
	params.SetBody(body)

	resp, err := s.twilioClient.Api.CreateMessage(params)
	if err != nil {
		return nil, SMSResult{}, fmt.Errorf("failed to send SMS: %w", err)
	}

	status := ""
	if resp.Status != nil {
		status = *resp.Status
	}

	return nil, SMSResult{
		Line:       lineName,
		From:       from,
		To:         to,
		MessageSID: safeString(resp.Sid),
		Status:     status,
	}, nil
}

func (s *Service) placeCall(ctx context.Context, lineName, from, to, message string, repeatCount int) (*mcp.CallToolResult, CallResult, error) {
	speechText, err := prepareSpeechText(message, repeatCount)
	if err != nil {
		return nil, CallResult{}, err
	}

	audioURL, objectName, err := s.generateAndUploadAudio(ctx, speechText)
	if err != nil {
		return nil, CallResult{}, err
	}

	twiml, err := buildPlayTwiml(audioURL)
	if err != nil {
		return nil, CallResult{}, err
	}

	params := &api.CreateCallParams{}
	params.SetTo(to)
	params.SetFrom(from)
	params.SetTwiml(twiml)
	params.SetTimeout(30)

	resp, err := s.twilioClient.Api.CreateCall(params)
	if err != nil {
		return nil, CallResult{}, fmt.Errorf("failed to place call: %w", err)
	}

	status := ""
	if resp.Status != nil {
		status = *resp.Status
	}

	return nil, CallResult{
		Line:       lineName,
		From:       from,
		To:         to,
		CallSID:    safeString(resp.Sid),
		Status:     status,
		AudioURL:   audioURL,
		ObjectName: objectName,
	}, nil
}

func (s *Service) generateAndUploadAudio(ctx context.Context, text string) (string, string, error) {
	resp, err := s.openAIClient.Audio.Speech.New(ctx, openai.AudioSpeechNewParams{
		Input:          text,
		Model:          s.cfg.OpenAITTSModel,
		Voice:          openai.AudioSpeechNewParamsVoice(s.cfg.OpenAITTSVoice),
		ResponseFormat: openai.AudioSpeechNewParamsResponseFormatMP3,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to generate speech: %w", err)
	}
	defer resp.Body.Close()

	objectName := buildObjectName(s.cfg.GCSObjectPrefix)
	objectHandle := s.storageClient.Bucket(s.cfg.GCSBucketName).Object(objectName)
	writer := objectHandle.NewWriter(ctx)
	writer.ContentType = "audio/mpeg"
	writer.CacheControl = "public, max-age=86400"

	if _, err := io.Copy(writer, resp.Body); err != nil {
		_ = writer.Close()
		return "", "", fmt.Errorf("failed to upload generated audio: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", "", fmt.Errorf("failed to finalize uploaded audio: %w", err)
	}

	if !s.cfg.GCSSkipObjectACL {
		if err := objectHandle.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err != nil {
			if !isIgnorablePublicAccessError(err) {
				return "", "", fmt.Errorf("failed to make uploaded audio public: %w", err)
			}
			s.logger.Warn("object ACL update skipped; relying on bucket-level public access", "error", err)
		}
	}

	publicURL, err := buildPublicObjectURL(s.cfg.GCSPublicBaseURL, s.cfg.GCSBucketName, objectName)
	if err != nil {
		return "", "", err
	}

	return publicURL, objectName, nil
}

func normalizePhoneNumber(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("phone number is required")
	}

	parsed, err := phonenumbers.Parse(trimmed, "US")
	if err != nil {
		return "", fmt.Errorf("invalid phone number: %w", err)
	}
	if !phonenumbers.IsValidNumber(parsed) {
		return "", errors.New("phone number is not valid")
	}

	return phonenumbers.Format(parsed, phonenumbers.E164), nil
}

func requireUSOrPRNumber(raw string) error {
	parsed, err := phonenumbers.Parse(raw, "US")
	if err != nil {
		return fmt.Errorf("invalid phone number: %w", err)
	}

	region := phonenumbers.GetRegionCodeForNumber(parsed)
	if region != "US" && region != "PR" {
		return fmt.Errorf("only US or Puerto Rico numbers are allowed, got region %s", region)
	}
	return nil
}

func prepareSpeechText(message string, repeatCount int) (string, error) {
	body := sanitizeLine(message, maxCallChars)
	if body == "" {
		return "", errors.New("message is required")
	}

	if repeatCount == 0 {
		repeatCount = 2
	}
	if repeatCount < 1 || repeatCount > 5 {
		return "", errors.New("repeat_count must be between 1 and 5")
	}

	parts := make([]string, 0, repeatCount)
	for i := 0; i < repeatCount; i++ {
		parts = append(parts, body)
	}
	joined := strings.Join(parts, " ")
	if len(joined) > 4096 {
		return "", errors.New("spoken message is too long after repetition")
	}
	return joined, nil
}

func sanitizeLine(input string, maxLen int) string {
	return sanitize(input, maxLen, true)
}

func sanitizeMultiline(input string, maxLen int) string {
	return sanitize(input, maxLen, false)
}

func sanitize(input string, maxLen int, collapseWhitespace bool) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(input) {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}

	cleaned := b.String()
	if collapseWhitespace {
		cleaned = strings.Join(strings.Fields(cleaned), " ")
	}
	if maxLen > 0 && len(cleaned) > maxLen {
		cleaned = cleaned[:maxLen]
	}
	return strings.TrimSpace(cleaned)
}

func sanitizeTopic(topic string) string {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return ""
	}

	var b strings.Builder
	for _, r := range topic {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func buildObjectName(prefix string) string {
	name := uuid.NewString() + ".mp3"
	if prefix == "" {
		return name
	}
	return path.Join(prefix, name)
}

func buildPublicObjectURL(baseURL, bucketName, objectName string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid GCS_PUBLIC_BASE_URL: %w", err)
	}
	parsed.Path = path.Join(parsed.Path, bucketName, objectName)
	return parsed.String(), nil
}

func buildPlayTwiml(audioURL string) (string, error) {
	type playResponse struct {
		XMLName xml.Name  `xml:"Response"`
		Play    string    `xml:"Play"`
		Hangup  *struct{} `xml:"Hangup"`
	}

	payload, err := xml.Marshal(playResponse{
		Play:   audioURL,
		Hangup: &struct{}{},
	})
	if err != nil {
		return "", fmt.Errorf("failed to build TwiML: %w", err)
	}
	return xml.Header + string(payload), nil
}

func isIgnorablePublicAccessError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "uniform bucket-level access") ||
		strings.Contains(msg, "public access prevention")
}

func safeString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
