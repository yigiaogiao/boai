// Package ai contains the small provider boundary used by BOAI's optional
// bring-your-own-key features.
//
// Only one concrete interface is implemented in the first release: an
// OpenAI-compatible chat-completions and audio-transcriptions endpoint. The
// text and speech sides are separate interfaces because users may configure
// them independently, and a deployment may later add a different provider on
// one side without changing the other.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"boai/internal/safehttp"
)

// Provider names the API shape. The only accepted value in this release.
const OpenAICompatible = "openai-compatible"

var (
	// ErrNotConfigured reports a missing text or speech configuration.
	ErrNotConfigured = errors.New("ai: provider is not configured")
	// ErrResponse reports a provider response that could not be used.
	ErrResponse = errors.New("ai: provider returned an unusable response")
)

// Config is one decrypted provider configuration held in memory only while a
// request runs. It is never serialized by this package.
type Config struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
}

// Message is one chat turn.
type Message struct {
	Role    string
	Content string
}

// TextRequest asks a text provider for a completion.
type TextRequest struct {
	Messages  []Message
	MaxTokens int
	JSON      bool
}

// SpeechRequest asks a speech provider to transcribe audio.
type SpeechRequest struct {
	Filename    string
	ContentType string
	Audio       []byte
}

// SpeechSegment is one timed transcription line.
type SpeechSegment struct {
	Start float64
	End   float64
	Text  string
}

// SpeechResponse is a normalized transcription result.
type SpeechResponse struct {
	Text     string
	Language string
	Segments []SpeechSegment
}

// TextProvider is the provider-independent text interface.
type TextProvider interface {
	Complete(ctx context.Context, cfg Config, req TextRequest) (string, error)
	TestText(ctx context.Context, cfg Config) error
}

// SpeechProvider is the provider-independent speech interface.
type SpeechProvider interface {
	Transcribe(ctx context.Context, cfg Config, req SpeechRequest) (SpeechResponse, error)
	TestSpeech(ctx context.Context, cfg Config) error
}

// OpenAI is an OpenAI-compatible implementation of both interfaces.
type OpenAI struct {
	client *safehttp.Client
}

// NewOpenAI builds the provider over the same guarded HTTP client used for
// every other external request. Redirects, DNS and private-address checks are
// therefore not bypassed by a provider URL.
func NewOpenAI(client *safehttp.Client) *OpenAI {
	return &OpenAI{client: client}
}

// NormalizeBaseURL validates and trims a user-supplied provider base URL.
func NormalizeBaseURL(raw string, allowPrivate bool) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := safehttp.ValidateURL(trimmed, allowPrivate)
	if err != nil {
		return "", errors.New("供应商地址必须是公开的 HTTP(S) 地址")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("供应商地址不能带查询参数或锚点")
	}
	return trimmed, nil
}

// Complete performs a chat-completions call.
func (p *OpenAI) Complete(ctx context.Context, cfg Config, req TextRequest) (string, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return "", ErrNotConfigured
	}
	messages := make([]map[string]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		messages = append(messages, map[string]string{"role": message.Role, "content": message.Content})
	}
	body := map[string]any{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": 0.2,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.JSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	raw, err := p.postJSON(ctx, cfg, "/chat/completions", body)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%w: chat response is not JSON", ErrResponse)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%w: chat response has no choices", ErrResponse)
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("%w: chat response is empty", ErrResponse)
	}
	return content, nil
}

// TestText sends a minimal prompt. It intentionally does not use a
// provider-specific model-list endpoint, so the same test works with any
// compatible service.
func (p *OpenAI) TestText(ctx context.Context, cfg Config) error {
	_, err := p.Complete(ctx, cfg, TextRequest{
		Messages:  []Message{{Role: "user", Content: "只回复 OK"}},
		MaxTokens: 8,
	})
	return err
}

// Transcribe uploads audio to an OpenAI-compatible transcription endpoint.
func (p *OpenAI) Transcribe(ctx context.Context, cfg Config, req SpeechRequest) (SpeechResponse, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return SpeechResponse{}, ErrNotConfigured
	}
	if len(req.Audio) == 0 {
		return SpeechResponse{}, errors.New("ai: empty audio")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", req.Filename)
	if err != nil {
		return SpeechResponse{}, err
	}
	if _, err := part.Write(req.Audio); err != nil {
		return SpeechResponse{}, err
	}
	if err := writer.WriteField("model", cfg.Model); err != nil {
		return SpeechResponse{}, err
	}
	if err := writer.WriteField("response_format", "verbose_json"); err != nil {
		return SpeechResponse{}, err
	}
	if err := writer.Close(); err != nil {
		return SpeechResponse{}, err
	}
	endpoint, err := endpointURL(cfg, "/audio/transcriptions")
	if err != nil {
		return SpeechResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return SpeechResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	raw, err := p.do(request)
	if err != nil {
		return SpeechResponse{}, err
	}
	return parseTranscription(raw)
}

// TestSpeech uploads a short silent WAV. The caller must disclose that a
// connection test may incur a small provider charge.
func (p *OpenAI) TestSpeech(ctx context.Context, cfg Config) error {
	silence := silentWAV(0.25)
	_, err := p.Transcribe(ctx, cfg, SpeechRequest{
		Filename:    "boai-test.wav",
		ContentType: "audio/wav",
		Audio:       silence,
	})
	return err
}

func (p *OpenAI) postJSON(ctx context.Context, cfg Config, suffix string, body any) ([]byte, error) {
	endpoint, err := endpointURL(cfg, suffix)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	request.Header.Set("Content-Type", "application/json")
	return p.do(request)
}

func (p *OpenAI) do(request *http.Request) ([]byte, error) {
	resp, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("连接供应商失败：%s", safeMessage(err))
	}
	defer resp.Body.Close()
	raw, err := safehttp.ReadAll(resp.Body, 2<<20)
	if err != nil {
		return nil, fmt.Errorf("读取供应商响应失败：%s", safeMessage(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("供应商返回 HTTP %d：%s", resp.StatusCode, providerError(raw))
	}
	return raw, nil
}

func endpointURL(cfg Config, suffix string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return "", ErrNotConfigured
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("供应商地址无效")
	}
	return base + suffix, nil
}

func parseTranscription(raw []byte) (SpeechResponse, error) {
	var out SpeechResponse
	var payload struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		// Some compatible servers return plain text when verbose_json is
		// ignored. Accept that rather than throwing away a usable result.
		text := strings.TrimSpace(string(raw))
		if text == "" || strings.HasPrefix(text, "{") {
			return SpeechResponse{}, fmt.Errorf("%w: transcription response is not JSON", ErrResponse)
		}
		return SpeechResponse{Text: text, Segments: []SpeechSegment{{Start: 0, End: 0, Text: text}}}, nil
	}
	if payload.Error.Message != "" {
		return SpeechResponse{}, fmt.Errorf("供应商转写失败：%s", trimMessage(payload.Error.Message))
	}
	out.Text = strings.TrimSpace(payload.Text)
	out.Language = strings.TrimSpace(payload.Language)
	for _, segment := range payload.Segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		if segment.End < segment.Start {
			segment.End = segment.Start
		}
		out.Segments = append(out.Segments, SpeechSegment{Start: segment.Start, End: segment.End, Text: text})
	}
	if len(out.Segments) == 0 && out.Text != "" {
		out.Segments = []SpeechSegment{{Start: 0, End: 0, Text: out.Text}}
	}
	if len(out.Segments) == 0 {
		return SpeechResponse{}, fmt.Errorf("%w: transcription response has no text", ErrResponse)
	}
	return out, nil
}

func providerError(raw []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &payload) == nil {
		if payload.Error.Message != "" {
			return trimMessage(payload.Error.Message)
		}
		if payload.Message != "" {
			return trimMessage(payload.Message)
		}
	}
	return trimMessage(strings.TrimSpace(string(raw)))
}

func trimMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len([]rune(value)) > 300 {
		value = string([]rune(value)[:300]) + "…"
	}
	if value == "" {
		return "未提供错误信息"
	}
	return value
}

func safeMessage(err error) string {
	if errors.Is(err, safehttp.ErrBlocked) {
		return "地址不在允许的公开网络范围"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时"
	}
	return trimMessage(err.Error())
}

// silentWAV builds a short PCM WAV payload for connection tests. It contains
// no user content.
func silentWAV(seconds float64) []byte {
	const sampleRate = 8000
	frames := int(seconds * sampleRate)
	dataSize := frames * 2
	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	putLE32(buf[4:8], uint32(36+dataSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	putLE32(buf[16:20], 16)
	putLE16(buf[20:22], 1)
	putLE16(buf[22:24], 1)
	putLE32(buf[24:28], sampleRate)
	putLE32(buf[28:32], sampleRate*2)
	putLE16(buf[32:34], 2)
	putLE16(buf[34:36], 16)
	copy(buf[36:40], "data")
	putLE32(buf[40:44], uint32(dataSize))
	return buf
}

func putLE16(dst []byte, value uint16) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
}

func putLE32(dst []byte, value uint32) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value >> 16)
	dst[3] = byte(value >> 24)
}

// Timeout is the default full-operation bound for provider calls.
const Timeout = 5 * time.Minute

var _ TextProvider = (*OpenAI)(nil)
var _ SpeechProvider = (*OpenAI)(nil)
