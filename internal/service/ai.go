package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"boai/internal/ai"
	"boai/internal/store"
)

const (
	AIKindText   = "text"
	AIKindSpeech = "speech"

	maxAIKeyRunes     = 4096
	maxAIBaseRunes    = 500
	maxAIModelRunes   = 200
	maxAskRunes       = 1000
	maxTargetRunes    = 40
	maxPromptRunes    = 48000
	maxAudioBytes     = 80 << 20
	maxTaskErrorRunes = 500

	insightSummary  = "summary"
	insightAnalysis = "analysis"
	insightMap      = "map"
)

var (
	// ErrAIUnavailable reports a missing configuration or encryption secret.
	ErrAIUnavailable = errors.New("AI 服务尚未配置")
	// ErrAIInput reports an invalid request before anything is sent.
	ErrAIInput = errors.New("AI 请求参数无效")
)

// AIUnavailableError carries the user-facing reason a provider cannot be used.
type AIUnavailableError struct {
	Message string
}

func (e *AIUnavailableError) Error() string { return e.Message }
func (e *AIUnavailableError) Unwrap() error { return ErrAIUnavailable }

// AIProviderStatus is the non-sensitive view of one provider configuration.
type AIProviderStatus struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	KeySet     bool   `json:"key_set"`
	UpdatedAt  int64  `json:"updated_at"`
}

// AIStatusView is returned by the settings and status endpoints. API keys are
// never included.
type AIStatusView struct {
	Configured  bool             `json:"configured"`
	Text        AIProviderStatus `json:"text"`
	Speech      AIProviderStatus `json:"speech"`
	SecretReady bool             `json:"secret_ready"`
	Message     string           `json:"message"`
}

// AIConfigInput is a settings form submission.
type AIConfigInput struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
}

// AITestResult reports a connection test without provider secrets.
type AITestResult struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	Disclosure string `json:"disclosure"`
}

// CitationView points back to one transcript segment.
type CitationView struct {
	Segment int     `json:"segment"`
	Start   float64 `json:"start"`
	Quote   string  `json:"quote"`
}

// InsightView is a stored generated guide, analysis or map.
type InsightView struct {
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	Model     string          `json:"model"`
	Source    string          `json:"source"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
}

// QAView is one stored question and answer.
type QAView struct {
	ID         int64           `json:"id"`
	Question   string          `json:"question"`
	Answer     string          `json:"answer"`
	Citations  json.RawMessage `json:"citations"`
	Grounded   bool            `json:"grounded"`
	Model      string          `json:"model"`
	CreatedAt  int64           `json:"created_at"`
	CostNotice string          `json:"cost_notice"`
}

// TaskView is a persisted job with a disclosure for the UI.
type TaskView struct {
	ID         string  `json:"id"`
	EpisodeID  int64   `json:"episode_id"`
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	Provider   string  `json:"provider"`
	Error      string  `json:"error"`
	Progress   float64 `json:"progress"`
	Payload    string  `json:"payload"`
	Attempts   int     `json:"attempts"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at"`
	Sends      string  `json:"sends"`
	CostNotice string  `json:"cost_notice"`
}

// EpisodeAI is the per-episode generated-content bundle.
type EpisodeAI struct {
	Insights  []InsightView `json:"insights"`
	Questions []QAView      `json:"questions"`
	Tasks     []TaskView    `json:"tasks"`
}

// AIStatus returns one owner's non-sensitive provider state.
func (s *Service) AIStatus(ctx context.Context, owner string) (AIStatusView, error) {
	configs, err := s.store.ListAIConfigs(ctx, owner)
	if err != nil {
		return AIStatusView{}, err
	}
	out := AIStatusView{SecretReady: strings.TrimSpace(s.secret) != ""}
	for _, config := range configs {
		status := AIProviderStatus{
			Configured: config.APIKeyEnc != "",
			Provider:   config.Provider,
			BaseURL:    config.BaseURL,
			Model:      config.Model,
			KeySet:     config.APIKeyEnc != "",
			UpdatedAt:  config.UpdatedAt,
		}
		switch config.Kind {
		case AIKindText:
			out.Text = status
		case AIKindSpeech:
			out.Speech = status
		}
	}
	out.Configured = out.Text.Configured || out.Speech.Configured
	switch {
	case !out.SecretReady:
		out.Message = "服务端缺少加密密钥，暂时不能保存 API Key。"
	case !out.Configured:
		out.Message = "可选：分别配置文本模型和语音转写服务。Key 只保存在本机，费用由服务商收取。"
	default:
		out.Message = "AI 功能可选；Key 只保存在本机，费用由服务商收取。"
	}
	return out, nil
}

// SaveAIConfig stores one provider configuration. An empty API key keeps the
// existing key, so users can change model or base URL without re-entering it.
func (s *Service) SaveAIConfig(ctx context.Context, owner, kind string, input AIConfigInput) (AIProviderStatus, error) {
	if err := validateAIKind(kind); err != nil {
		return AIProviderStatus{}, err
	}
	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		provider = ai.OpenAICompatible
	}
	if provider != ai.OpenAICompatible {
		return AIProviderStatus{}, fmt.Errorf("%w: 首版只支持 OpenAI 兼容接口", ErrAIInput)
	}
	baseURL, err := ai.NormalizeBaseURL(input.BaseURL, s.allowInsecureAI)
	if err != nil {
		return AIProviderStatus{}, fmt.Errorf("%w: %s", ErrAIInput, err.Error())
	}
	model := strings.TrimSpace(input.Model)
	if model == "" || len([]rune(model)) > maxAIModelRunes {
		return AIProviderStatus{}, fmt.Errorf("%w: 请填写模型名称", ErrAIInput)
	}
	if len([]rune(baseURL)) > maxAIBaseRunes {
		return AIProviderStatus{}, fmt.Errorf("%w: 供应商地址过长", ErrAIInput)
	}
	if strings.TrimSpace(s.secret) == "" {
		return AIProviderStatus{}, &AIUnavailableError{Message: "服务端未配置加密密钥，不能保存 API Key。"}
	}
	key := strings.TrimSpace(input.APIKey)
	var encrypted string
	existing, err := s.store.GetAIConfig(ctx, owner, kind)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return AIProviderStatus{}, err
	}
	if key == "" {
		if errors.Is(err, store.ErrNotFound) || existing.APIKeyEnc == "" {
			return AIProviderStatus{}, fmt.Errorf("%w: 首次保存需要填写 API Key", ErrAIInput)
		}
		encrypted = existing.APIKeyEnc
	} else {
		if len([]rune(key)) > maxAIKeyRunes {
			return AIProviderStatus{}, fmt.Errorf("%w: API Key 过长", ErrAIInput)
		}
		encrypted, err = ai.EncryptKey(s.secret, key)
		if err != nil {
			return AIProviderStatus{}, &AIUnavailableError{Message: "无法加密 API Key，请检查服务端密钥配置。"}
		}
	}
	now := s.now().Unix()
	if err := s.store.SaveAIConfig(ctx, owner, kind, provider, baseURL, model, encrypted, now); err != nil {
		return AIProviderStatus{}, err
	}
	return AIProviderStatus{
		Configured: true, Provider: provider, BaseURL: baseURL, Model: model,
		KeySet: true, UpdatedAt: now,
	}, nil
}

// TestAIConfig performs a minimal provider call. The key is decrypted only
// in memory and is never written to a response or a log.
func (s *Service) TestAIConfig(ctx context.Context, owner, kind string) (AITestResult, error) {
	config, err := s.providerConfig(ctx, owner, kind)
	if err != nil {
		return AITestResult{}, err
	}
	testCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var verifyErr error
	disclosure := ""
	if kind == AIKindText {
		verifyErr = s.text.TestText(testCtx, config)
		disclosure = "测试会向供应商发送一句很短的测试文本（不含本集内容），可能产生少量费用。"
	} else {
		verifyErr = s.speech.TestSpeech(testCtx, config)
		disclosure = "测试会向供应商上传约 0.25 秒静音音频（不含本集内容），可能产生少量费用。"
	}
	if verifyErr != nil {
		return AITestResult{OK: false, Message: verifyErr.Error(), Disclosure: disclosure}, nil
	}
	label := "文本模型"
	if kind == AIKindSpeech {
		label = "语音转写"
	}
	return AITestResult{OK: true, Message: label + "连接成功", Disclosure: disclosure}, nil
}

// DeleteAIConfig removes a key. Generated insights, answers and transcripts
// remain readable.
func (s *Service) DeleteAIConfig(ctx context.Context, owner, kind string) error {
	if err := validateAIKind(kind); err != nil {
		return err
	}
	return s.store.DeleteAIConfig(ctx, owner, kind)
}

// ListEpisodeAI returns stored results and task states for one episode.
func (s *Service) ListEpisodeAI(ctx context.Context, owner string, episodeID int64) (EpisodeAI, error) {
	if _, err := s.store.GetEpisode(ctx, owner, episodeID); err != nil {
		return EpisodeAI{}, err
	}
	insights, err := s.store.ListInsights(ctx, owner, episodeID)
	if err != nil {
		return EpisodeAI{}, err
	}
	questions, err := s.store.ListQA(ctx, owner, episodeID)
	if err != nil {
		return EpisodeAI{}, err
	}
	tasks, err := s.store.ListTasks(ctx, owner, episodeID)
	if err != nil {
		return EpisodeAI{}, err
	}
	out := EpisodeAI{Insights: []InsightView{}, Questions: []QAView{}, Tasks: []TaskView{}}
	for _, insight := range insights {
		out.Insights = append(out.Insights, InsightView{
			Kind: insight.Kind, Payload: validRaw(insight.Payload), Model: insight.Model,
			Source: insight.Source, CreatedAt: insight.CreatedAt, UpdatedAt: insight.UpdatedAt,
		})
	}
	for _, question := range questions {
		out.Questions = append(out.Questions, QAView{
			ID: question.ID, Question: question.Question, Answer: question.Answer,
			Citations: validRaw(question.Citations), Grounded: question.Grounded, Model: question.Model,
			CreatedAt: question.CreatedAt, CostNotice: "答案由文本模型生成，费用由服务商收取。",
		})
	}
	for _, task := range tasks {
		out.Tasks = append(out.Tasks, toTaskView(task))
	}
	return out, nil
}

// EnqueueInsight starts summary, analysis or map generation.
func (s *Service) EnqueueInsight(ctx context.Context, owner string, episodeID int64, kind string) (TaskView, error) {
	switch kind {
	case insightSummary, insightAnalysis, insightMap:
	default:
		return TaskView{}, fmt.Errorf("%w: 未知的生成类型", ErrAIInput)
	}
	config, err := s.providerConfig(ctx, owner, AIKindText)
	if err != nil {
		return TaskView{}, err
	}
	episode, err := s.store.GetEpisode(ctx, owner, episodeID)
	if err != nil {
		return TaskView{}, err
	}
	_, transcriptErr := s.store.GetTranscript(ctx, owner, episodeID)
	if transcriptErr != nil && !errors.Is(transcriptErr, store.ErrNotFound) {
		return TaskView{}, transcriptErr
	}
	if strings.TrimSpace(stripMarkup(episode.Description)) == "" && errors.Is(transcriptErr, store.ErrNotFound) {
		return TaskView{}, fmt.Errorf("%w: 本集没有节目简介或逐字稿，无法生成依据充分的内容", ErrAIInput)
	}
	task := store.Task{
		ID: newTaskID(), Owner: owner, EpisodeID: episodeID, Kind: kind, Status: "queued",
		Provider: providerLabel(config), Payload: "{}", CreatedAt: s.now().Unix(),
	}
	return s.enqueueTask(ctx, task)
}

// EnqueueTranslation starts a text-only translation of an existing transcript.
func (s *Service) EnqueueTranslation(ctx context.Context, owner string, episodeID int64, target string) (TaskView, error) {
	config, err := s.providerConfig(ctx, owner, AIKindText)
	if err != nil {
		return TaskView{}, err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		target = "中文（简体）"
	}
	if len([]rune(target)) > maxTargetRunes {
		return TaskView{}, fmt.Errorf("%w: 目标语言过长", ErrAIInput)
	}
	if _, err := s.store.GetTranscript(ctx, owner, episodeID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return TaskView{}, fmt.Errorf("%w: 本集还没有逐字稿，无法翻译", ErrAIInput)
		}
		return TaskView{}, err
	}
	payload, _ := json.Marshal(map[string]string{"target_language": target})
	task := store.Task{
		ID: newTaskID(), Owner: owner, EpisodeID: episodeID, Kind: "translation", Status: "queued",
		Provider: providerLabel(config), Payload: string(payload), CreatedAt: s.now().Unix(),
	}
	return s.enqueueTask(ctx, task)
}

// EnqueueTranscription starts cloud transcription only when no stored
// transcript exists. Source-provided transcripts are always preferred.
func (s *Service) EnqueueTranscription(ctx context.Context, owner string, episodeID int64) (TaskView, error) {
	config, err := s.providerConfig(ctx, owner, AIKindSpeech)
	if err != nil {
		return TaskView{}, err
	}
	if _, err := s.store.GetTranscript(ctx, owner, episodeID); err == nil {
		return TaskView{}, fmt.Errorf("%w: 本集已有逐字稿，无需云端转写", ErrAIInput)
	} else if !errors.Is(err, store.ErrNotFound) {
		return TaskView{}, err
	}
	audioURL, _, err := s.store.EpisodeAudio(ctx, owner, episodeID)
	if err != nil {
		return TaskView{}, err
	}
	if strings.TrimSpace(audioURL) == "" {
		return TaskView{}, fmt.Errorf("%w: 本集没有可转写的音频地址", ErrAIInput)
	}
	task := store.Task{
		ID: newTaskID(), Owner: owner, EpisodeID: episodeID, Kind: "transcription", Status: "queued",
		Provider: providerLabel(config), Payload: "{}", CreatedAt: s.now().Unix(),
	}
	return s.enqueueTask(ctx, task)
}

// EnqueueQuestion starts a current-episode-only question.
func (s *Service) EnqueueQuestion(ctx context.Context, owner string, episodeID int64, question string) (TaskView, error) {
	config, err := s.providerConfig(ctx, owner, AIKindText)
	if err != nil {
		return TaskView{}, err
	}
	question = strings.TrimSpace(question)
	if question == "" || len([]rune(question)) > maxAskRunes {
		return TaskView{}, fmt.Errorf("%w: 问题不能为空，且不超过 %d 字", ErrAIInput, maxAskRunes)
	}
	if _, err := s.store.GetTranscript(ctx, owner, episodeID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return TaskView{}, fmt.Errorf("%w: 本集还没有逐字稿，先在逐字稿标签获取或转写", ErrAIInput)
		}
		return TaskView{}, err
	}
	payload, _ := json.Marshal(map[string]string{"question": question})
	task := store.Task{
		ID: newTaskID(), Owner: owner, EpisodeID: episodeID, Kind: "ask", Status: "queued",
		Provider: providerLabel(config), Payload: string(payload), CreatedAt: s.now().Unix(),
	}
	return s.enqueueTask(ctx, task)
}

// RetryTask requeues a failed task. Completed tasks are not retried.
func (s *Service) RetryTask(ctx context.Context, owner, taskID string) (TaskView, error) {
	task, err := s.store.GetTask(ctx, owner, taskID)
	if err != nil {
		return TaskView{}, err
	}
	if task.Status != "failed" {
		return TaskView{}, fmt.Errorf("%w: 只有失败的任务可以重试", ErrAIInput)
	}
	ok, err := s.store.RetryTask(ctx, owner, taskID, s.now().Unix())
	if err != nil {
		return TaskView{}, err
	}
	if !ok {
		return TaskView{}, fmt.Errorf("%w: 任务状态已变化，请刷新", ErrAIInput)
	}
	task.Status = "queued"
	task.Error = ""
	task.FinishedAt = 0
	go s.runTask(task)
	return toTaskView(task), nil
}

func (s *Service) enqueueTask(ctx context.Context, task store.Task) (TaskView, error) {
	if existing, err := s.store.FindActiveTask(ctx, task.Owner, task.EpisodeID, task.Kind, task.Payload); err == nil {
		return toTaskView(existing), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return TaskView{}, err
	}
	created, err := s.store.CreateTask(ctx, task)
	if err != nil {
		// A concurrent request may have won the unique active-task index.
		if existing, findErr := s.store.FindActiveTask(ctx, task.Owner, task.EpisodeID, task.Kind, task.Payload); findErr == nil {
			return toTaskView(existing), nil
		}
		return TaskView{}, err
	}
	go s.runTask(created)
	return toTaskView(created), nil
}

func (s *Service) runTask(task store.Task) {
	ctx, cancel := context.WithTimeout(context.Background(), s.aiTimeout)
	defer cancel()
	started, err := s.store.StartTask(ctx, task.Owner, task.ID, task.Provider, s.now().Unix())
	if err != nil || !started {
		return
	}
	finished := false
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = s.store.FinishTask(context.Background(), task.Owner, task.ID, "failed",
				"生成过程异常中断，可重试", s.now().Unix())
		} else if !finished {
			_ = s.store.FinishTask(context.Background(), task.Owner, task.ID, "failed",
				"任务中断，可重试", s.now().Unix())
		}
	}()

	var runErr error
	switch task.Kind {
	case insightSummary, insightAnalysis, insightMap:
		runErr = s.runInsight(ctx, task)
	case "translation":
		runErr = s.runTranslation(ctx, task)
	case "transcription":
		runErr = s.runTranscription(ctx, task)
	case "ask":
		runErr = s.runQuestion(ctx, task)
	default:
		runErr = errors.New("未知的任务类型")
	}
	finished = true
	if runErr != nil {
		_ = s.store.FinishTask(context.Background(), task.Owner, task.ID, "failed",
			truncateText(runErr.Error(), maxTaskErrorRunes), s.now().Unix())
		return
	}
	_ = s.store.FinishTask(context.Background(), task.Owner, task.ID, "completed", "", s.now().Unix())
}

func (s *Service) runInsight(ctx context.Context, task store.Task) error {
	config, err := s.providerConfig(ctx, task.Owner, AIKindText)
	if err != nil {
		return err
	}
	episode, err := s.store.GetEpisode(ctx, task.Owner, task.EpisodeID)
	if err != nil {
		return err
	}
	transcript, transcriptErr := s.store.GetTranscript(ctx, task.Owner, task.EpisodeID)
	if transcriptErr != nil && !errors.Is(transcriptErr, store.ErrNotFound) {
		return transcriptErr
	}
	contextBlock, segments := episodeContext(episode, transcript, transcriptErr == nil)
	var payload any
	switch task.Kind {
	case insightSummary:
		payload, err = s.generateSummary(ctx, config, contextBlock, segments)
	case insightAnalysis:
		payload, err = s.generateAnalysis(ctx, config, contextBlock, segments)
	case insightMap:
		payload, err = s.generateMap(ctx, config, contextBlock, segments)
	default:
		return errors.New("未知的生成类型")
	}
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.store.SaveInsight(ctx, task.Owner, store.Insight{
		EpisodeID: task.EpisodeID, Kind: task.Kind, Payload: string(encoded),
		Model: config.Model, Source: "ai",
	}, s.now().Unix())
	return err
}

func (s *Service) runQuestion(ctx context.Context, task store.Task) error {
	config, err := s.providerConfig(ctx, task.Owner, AIKindText)
	if err != nil {
		return err
	}
	var input struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &input); err != nil || strings.TrimSpace(input.Question) == "" {
		return errors.New("问题内容缺失")
	}
	episode, err := s.store.GetEpisode(ctx, task.Owner, task.EpisodeID)
	if err != nil {
		return err
	}
	transcript, err := s.store.GetTranscript(ctx, task.Owner, task.EpisodeID)
	if err != nil {
		return err
	}
	contextBlock, segments := episodeContext(episode, transcript, true)
	answer, grounded, citations, err := s.generateAnswer(ctx, config, contextBlock, segments, input.Question)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(citations)
	if err != nil {
		return err
	}
	_, err = s.store.SaveQA(ctx, task.Owner, store.QA{
		EpisodeID: task.EpisodeID, Question: input.Question, Answer: answer,
		Citations: string(encoded), Grounded: grounded, Model: config.Model,
	}, s.now().Unix())
	return err
}

func (s *Service) runTranslation(ctx context.Context, task store.Task) error {
	config, err := s.providerConfig(ctx, task.Owner, AIKindText)
	if err != nil {
		return err
	}
	var input struct {
		Target string `json:"target_language"`
	}
	_ = json.Unmarshal([]byte(task.Payload), &input)
	if strings.TrimSpace(input.Target) == "" {
		input.Target = "中文（简体）"
	}
	transcript, err := s.store.GetTranscript(ctx, task.Owner, task.EpisodeID)
	if err != nil {
		return err
	}
	var lines strings.Builder
	valid := map[int]struct{}{}
	for _, segment := range transcript.Segments {
		if strings.TrimSpace(segment.Text) == "" {
			continue
		}
		valid[segment.Index] = struct{}{}
		fmt.Fprintf(&lines, "[%d] %s\n", segment.Index, segment.Text)
	}
	if len(valid) == 0 {
		return errors.New("逐字稿没有可翻译的文本")
	}
	system := "你是专业的播客翻译。逐行翻译用户提供的字幕，保持行号和顺序，不添加解释或总结。" +
		"只输出 JSON：{\"translations\":[{\"segment\":行号,\"text\":\"译文\"}]}。"
	user := fmt.Sprintf("目标语言：%s\n逐字稿：\n%s", input.Target, truncateText(lines.String(), maxPromptRunes))
	raw, err := s.text.Complete(ctx, config, ai.TextRequest{
		Messages:  []ai.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		MaxTokens: 4000, JSON: true,
	})
	if err != nil {
		return err
	}
	object, err := extractJSONObject(raw)
	if err != nil {
		return err
	}
	var output struct {
		Translations []struct {
			Segment int    `json:"segment"`
			Text    string `json:"text"`
		} `json:"translations"`
	}
	if err := json.Unmarshal(object, &output); err != nil {
		return errors.New("模型返回的译文格式无法解析")
	}
	translations := map[int]string{}
	for _, item := range output.Translations {
		if _, ok := valid[item.Segment]; !ok {
			continue
		}
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		translations[item.Segment] = text
	}
	if len(translations) == 0 {
		return errors.New("模型没有返回可用的译文")
	}
	return s.store.SaveTranscriptTranslations(ctx, task.Owner, task.EpisodeID, input.Target, translations)
}

func (s *Service) runTranscription(ctx context.Context, task store.Task) error {
	config, err := s.providerConfig(ctx, task.Owner, AIKindSpeech)
	if err != nil {
		return err
	}
	resp, contentType, err := s.OpenAudio(ctx, task.Owner, task.EpisodeID, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	audio, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes+1))
	if err != nil {
		return fmt.Errorf("读取音频失败：%v", err)
	}
	if len(audio) == 0 {
		return errors.New("音频为空，无法转写")
	}
	if len(audio) > maxAudioBytes {
		return errors.New("音频超过 80 MiB，首版不支持云端转写")
	}
	result, err := s.speech.Transcribe(ctx, config, ai.SpeechRequest{
		Filename:    "episode" + transcriptionExtension(contentType),
		ContentType: contentType,
		Audio:       audio,
	})
	if err != nil {
		return err
	}
	segments := make([]store.Segment, 0, len(result.Segments))
	for index, segment := range result.Segments {
		if strings.TrimSpace(segment.Text) == "" {
			continue
		}
		segments = append(segments, store.Segment{
			Index: index, Start: segment.Start, End: segment.End, Text: segment.Text,
		})
	}
	if len(segments) == 0 {
		return errors.New("转写服务没有返回文字")
	}
	timed := false
	for _, segment := range segments {
		if segment.End > segment.Start || segment.Start > 0 {
			timed = true
			break
		}
	}
	return s.store.SaveTranscript(ctx, task.Owner, task.EpisodeID, "AI 转写", result.Language,
		"", timed, segments)
}

func (s *Service) generateSummary(ctx context.Context, config ai.Config, contextBlock string, segments map[int]store.Segment) (any, error) {
	hasTranscript := len(segments) > 0
	instruction := `请输出 JSON：
{"overview":"总览","chapters":[{"segment":段落编号,"title":"章节标题"}],"points":[{"text":"核心观点","citations":[{"segment":段落编号,"quote":"原文连续摘录"}]}],"quotes":[{"segment":段落编号,"quote":"原文连续摘录"}]}
没有逐字稿时，chapters/points.citations/quotes 必须为空数组，不得编造时间点。`
	if !hasTranscript {
		instruction = `本集没有逐字稿。请只根据节目简介输出 JSON：
{"overview":"总览","chapters":[],"points":[{"text":"核心观点","citations":[]}],"quotes":[]}。
不得编造引文或时间点。`
	}
	raw, err := s.completeJSON(ctx, config, "你是播客导读编辑。只依据本集材料写作，不使用外部知识。", instruction+"\n\n本集材料：\n"+contextBlock, 2600)
	if err != nil {
		return nil, err
	}
	object, err := extractJSONObject(raw)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Overview string `json:"overview"`
		Chapters []struct {
			Segment *int     `json:"segment"`
			Start   *float64 `json:"start"`
			Title   string   `json:"title"`
			Label   string   `json:"label"`
		} `json:"chapters"`
		Points []struct {
			Text      string        `json:"text"`
			Citations []rawCitation `json:"citations"`
		} `json:"points"`
		Quotes []rawCitation `json:"quotes"`
	}
	if err := json.Unmarshal(object, &parsed); err != nil {
		return nil, errors.New("模型返回的导读格式无法解析")
	}
	overview := strings.TrimSpace(parsed.Overview)
	if overview == "" {
		return nil, errors.New("模型没有返回导读总览")
	}
	chapters := []map[string]any{}
	for _, chapter := range parsed.Chapters {
		label := strings.TrimSpace(chapter.Title)
		if label == "" {
			label = strings.TrimSpace(chapter.Label)
		}
		segment, ok := resolveSegment(chapter.Segment, chapter.Start, segments)
		if !ok || label == "" {
			continue
		}
		chapters = append(chapters, map[string]any{"start": segment.Start, "label": label})
	}
	points := []map[string]any{}
	for _, point := range parsed.Points {
		text := strings.TrimSpace(point.Text)
		if text == "" {
			continue
		}
		points = append(points, map[string]any{"text": text, "citations": validateCitations(point.Citations, segments)})
	}
	quotes := validateCitations(parsed.Quotes, segments)
	return map[string]any{
		"overview": overview,
		"chapters": chapters,
		"points":   points,
		"quotes":   quotes,
	}, nil
}

func (s *Service) generateAnalysis(ctx context.Context, config ai.Config, contextBlock string, segments map[int]store.Segment) (any, error) {
	instruction := `请输出 JSON：
{"title":"精读标题","summary":"一句话概括","sections":[{"heading":"主题","body":"展开分析","citations":[{"segment":段落编号,"quote":"原文连续摘录"}]}],"takeaways":[{"text":"可带走的结论","citations":[{"segment":段落编号,"quote":"原文连续摘录"}]}]}
只写本集出现的观点；证据不足时在正文中直接说明。`
	if len(segments) == 0 {
		instruction = `本集没有逐字稿。请只根据节目简介输出 JSON：
{"title":"精读标题","summary":"一句话概括","sections":[{"heading":"主题","body":"只基于简介的分析","citations":[]}],"takeaways":[]}。不得编造引文或时间点。`
	}
	raw, err := s.completeJSON(ctx, config, "你是严谨的播客精读编辑。只依据本集材料，不得引入外部事实。",
		instruction+"\n\n本集材料：\n"+contextBlock, 2800)
	if err != nil {
		return nil, err
	}
	object, err := extractJSONObject(raw)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Title    string `json:"title"`
		Summary  string `json:"summary"`
		Sections []struct {
			Heading   string        `json:"heading"`
			Body      string        `json:"body"`
			Citations []rawCitation `json:"citations"`
		} `json:"sections"`
		Takeaways []struct {
			Text      string        `json:"text"`
			Citations []rawCitation `json:"citations"`
		} `json:"takeaways"`
	}
	if err := json.Unmarshal(object, &parsed); err != nil {
		return nil, errors.New("模型返回的精读格式无法解析")
	}
	sections := []map[string]any{}
	for _, section := range parsed.Sections {
		heading := strings.TrimSpace(section.Heading)
		body := strings.TrimSpace(section.Body)
		if heading == "" && body == "" {
			continue
		}
		sections = append(sections, map[string]any{
			"heading": heading, "body": body, "citations": validateCitations(section.Citations, segments),
		})
	}
	if len(sections) == 0 {
		return nil, errors.New("模型没有返回可用的精读内容")
	}
	takeaways := []map[string]any{}
	for _, item := range parsed.Takeaways {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		takeaways = append(takeaways, map[string]any{
			"text": text, "citations": validateCitations(item.Citations, segments),
		})
	}
	return map[string]any{
		"title": strings.TrimSpace(parsed.Title), "summary": strings.TrimSpace(parsed.Summary),
		"sections": sections, "takeaways": takeaways,
	}, nil
}

func (s *Service) generateMap(ctx context.Context, config ai.Config, contextBlock string, segments map[int]store.Segment) (any, error) {
	instruction := `请输出 JSON：
{"title":"导图标题","branches":[{"title":"分支","children":[{"title":"子主题","citations":[{"segment":段落编号,"quote":"原文连续摘录"}]}]}]}
每个分支 2-5 个子主题，结构适合直接阅读。`
	if len(segments) == 0 {
		instruction = `本集没有逐字稿。请只根据节目简介输出 JSON：
{"title":"导图标题","branches":[{"title":"分支","children":[{"title":"子主题","citations":[]}]}]}。不得编造引文。`
	}
	raw, err := s.completeJSON(ctx, config, "你是播客结构编辑。只依据本集材料整理主题层级。",
		instruction+"\n\n本集材料：\n"+contextBlock, 2200)
	if err != nil {
		return nil, err
	}
	object, err := extractJSONObject(raw)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Title    string `json:"title"`
		Branches []struct {
			Title    string `json:"title"`
			Children []struct {
				Title     string        `json:"title"`
				Citations []rawCitation `json:"citations"`
			} `json:"children"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(object, &parsed); err != nil {
		return nil, errors.New("模型返回的导图格式无法解析")
	}
	branches := []map[string]any{}
	for _, branch := range parsed.Branches {
		title := strings.TrimSpace(branch.Title)
		if title == "" {
			continue
		}
		children := []map[string]any{}
		for _, child := range branch.Children {
			childTitle := strings.TrimSpace(child.Title)
			if childTitle == "" {
				continue
			}
			children = append(children, map[string]any{
				"title": childTitle, "citations": validateCitations(child.Citations, segments),
			})
		}
		if len(children) == 0 {
			continue
		}
		branches = append(branches, map[string]any{"title": title, "children": children})
	}
	if len(branches) == 0 {
		return nil, errors.New("模型没有返回可用的导图内容")
	}
	return map[string]any{"title": strings.TrimSpace(parsed.Title), "branches": branches}, nil
}

func (s *Service) generateAnswer(ctx context.Context, config ai.Config, contextBlock string, segments map[int]store.Segment, question string) (string, bool, []CitationView, error) {
	instruction := fmt.Sprintf(`用户问题：%s

只根据本集逐字稿回答。输出 JSON：
{"answer":"回答","grounded":true,"citations":[{"segment":段落编号,"quote":"原文连续摘录"}]}
如果逐字稿不足以回答，grounded 必须为 false，并在 answer 中直接说明证据不足。不得使用其它节目或外部知识。`, question)
	raw, err := s.completeJSON(ctx, config, "你是播客问答助手。答案必须能在提供的逐字稿中核对。",
		instruction+"\n\n本集材料：\n"+contextBlock, 1600)
	if err != nil {
		return "", false, nil, err
	}
	object, err := extractJSONObject(raw)
	if err != nil {
		return "", false, nil, err
	}
	var parsed struct {
		Answer    string        `json:"answer"`
		Grounded  bool          `json:"grounded"`
		Citations []rawCitation `json:"citations"`
	}
	if err := json.Unmarshal(object, &parsed); err != nil {
		return "", false, nil, errors.New("模型返回的问答格式无法解析")
	}
	answer := strings.TrimSpace(parsed.Answer)
	if answer == "" {
		return "", false, nil, errors.New("模型没有返回答案")
	}
	citations := validateCitations(parsed.Citations, segments)
	grounded := parsed.Grounded && len(citations) > 0
	return answer, grounded, citations, nil
}

func (s *Service) completeJSON(ctx context.Context, config ai.Config, system, user string, maxTokens int) (string, error) {
	return s.text.Complete(ctx, config, ai.TextRequest{
		Messages:  []ai.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		MaxTokens: maxTokens,
		JSON:      true,
	})
}

func (s *Service) providerConfig(ctx context.Context, owner, kind string) (ai.Config, error) {
	if err := validateAIKind(kind); err != nil {
		return ai.Config{}, err
	}
	if strings.TrimSpace(s.secret) == "" {
		return ai.Config{}, &AIUnavailableError{Message: "服务端未配置加密密钥，不能使用 API Key。"}
	}
	row, err := s.store.GetAIConfig(ctx, owner, kind)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.APIKeyEnc == "") {
		label := "文本模型"
		if kind == AIKindSpeech {
			label = "语音转写"
		}
		return ai.Config{}, &AIUnavailableError{Message: "尚未配置" + label + "，请先在设置中填写供应商、模型和 API Key。"}
	}
	if err != nil {
		return ai.Config{}, err
	}
	key, err := ai.DecryptKey(s.secret, row.APIKeyEnc)
	if err != nil {
		return ai.Config{}, &AIUnavailableError{Message: "无法读取已保存的 API Key，请重新保存配置。"}
	}
	return ai.Config{Provider: row.Provider, BaseURL: row.BaseURL, Model: row.Model, APIKey: key}, nil
}

func validateAIKind(kind string) error {
	if kind != AIKindText && kind != AIKindSpeech {
		return fmt.Errorf("%w: 未知的配置类型", ErrAIInput)
	}
	return nil
}

func providerLabel(config ai.Config) string {
	host := config.BaseURL
	if u, err := url.Parse(config.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	name := "OpenAI 兼容"
	if config.Provider != "" && config.Provider != ai.OpenAICompatible {
		name = config.Provider
	}
	return name + " · " + host
}

func toTaskView(task store.Task) TaskView {
	return TaskView{
		ID: task.ID, EpisodeID: task.EpisodeID, Kind: task.Kind, Status: task.Status,
		Provider: task.Provider, Error: task.Error, Progress: task.Progress, Payload: task.Payload,
		Attempts: task.Attempts, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		StartedAt: task.StartedAt, FinishedAt: task.FinishedAt, Sends: taskSends(task.Kind),
		CostNotice: taskCostNotice(task.Kind),
	}
}

func taskSends(kind string) string {
	if kind == "transcription" {
		return "audio"
	}
	return "text"
}

func taskCostNotice(kind string) string {
	if kind == "transcription" {
		return "会把本集音频发送给语音转写供应商，费用由服务商按音频时长收取。"
	}
	return "会把本集文字发送给文本模型供应商，费用由服务商收取。"
}

func newTaskID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

func validRaw(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

type rawCitation struct {
	Segment *int     `json:"segment"`
	Start   *float64 `json:"start"`
	Quote   string   `json:"quote"`
}

func validateCitations(items []rawCitation, segments map[int]store.Segment) []CitationView {
	out := []CitationView{}
	seen := map[string]struct{}{}
	for _, item := range items {
		segment, ok := resolveSegment(item.Segment, item.Start, segments)
		if !ok {
			continue
		}
		quote := strings.TrimSpace(item.Quote)
		if quote != "" && !quoteMatches(quote, segment.Text) {
			continue
		}
		if quote == "" {
			quote = truncateText(segment.Text, 160)
		}
		key := fmt.Sprintf("%d|%s", segment.Index, quote)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, CitationView{Segment: segment.Index, Start: segment.Start, Quote: quote})
	}
	return out
}

func resolveSegment(index *int, start *float64, segments map[int]store.Segment) (store.Segment, bool) {
	if len(segments) == 0 {
		return store.Segment{}, false
	}
	if index != nil {
		if segment, ok := segments[*index]; ok {
			return segment, true
		}
	}
	if start != nil {
		best := store.Segment{}
		bestDistance := 2.5
		for _, segment := range segments {
			distance := segment.Start - *start
			if distance < 0 {
				distance = -distance
			}
			if distance <= bestDistance {
				bestDistance = distance
				best = segment
			}
		}
		if bestDistance <= 2.5 {
			return best, true
		}
	}
	return store.Segment{}, false
}

func quoteMatches(quote, text string) bool {
	normalizedQuote := normalizeQuote(quote)
	normalizedText := normalizeQuote(text)
	if len([]rune(normalizedQuote)) < 2 {
		return false
	}
	return strings.Contains(normalizedText, normalizedQuote)
}

func normalizeQuote(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '，', '。', '！', '？', '、', ',', '.', '!', '?', ';', '；', ':', '：',
			'"', '\'', '“', '”', '‘', '’', '(', ')', '（', '）', '…':
			continue
		}
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}

func extractJSONObject(raw string) ([]byte, error) {
	content := strings.TrimSpace(raw)
	if strings.HasPrefix(content, "```") {
		if newline := strings.IndexByte(content, '\n'); newline >= 0 {
			content = content[newline+1:]
		}
		content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	}
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end < start {
		return nil, errors.New("模型没有返回可解析的 JSON")
	}
	object := []byte(content[start : end+1])
	if !json.Valid(object) {
		return nil, errors.New("模型返回的 JSON 不完整")
	}
	return object, nil
}

func episodeContext(episode store.Episode, transcript store.Transcript, hasTranscript bool) (string, map[int]store.Segment) {
	var builder strings.Builder
	fmt.Fprintf(&builder, "节目：%s\n单集标题：%s\n", episode.FeedTitle, episode.Title)
	description := strings.TrimSpace(stripMarkup(episode.Description))
	if description != "" {
		fmt.Fprintf(&builder, "节目简介：\n%s\n", truncateText(description, 4000))
	}
	if len(episode.Chapters) > 0 {
		builder.WriteString("节目源章节（仅作背景，不算逐字稿引用）：\n")
		for _, chapter := range episode.Chapters {
			fmt.Fprintf(&builder, "%s %s\n", clockSeconds(chapter.Start), chapter.Label)
		}
	}
	segments := map[int]store.Segment{}
	if !hasTranscript {
		builder.WriteString("本集没有逐字稿。\n")
		return builder.String(), segments
	}
	builder.WriteString("逐字稿段落（引用时使用方括号内的段落编号）：\n")
	used := 0
	indexes := make([]int, 0, len(transcript.Segments))
	for _, segment := range transcript.Segments {
		if strings.TrimSpace(segment.Text) == "" {
			continue
		}
		segments[segment.Index] = segment
		indexes = append(indexes, segment.Index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		segment := segments[index]
		line := fmt.Sprintf("[%d] %s %s\n", segment.Index, clockSeconds(int(segment.Start)), segment.Text)
		if used+len([]rune(line)) > maxPromptRunes {
			builder.WriteString("（逐字稿后文因长度限制省略）\n")
			break
		}
		builder.WriteString(line)
		used += len([]rune(line))
	}
	return builder.String(), segments
}

func stripMarkup(value string) string {
	var builder strings.Builder
	inTag := false
	for _, r := range value {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				builder.WriteRune(r)
			}
		}
	}
	replacer := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", "\"", "&#39;", "'")
	return strings.TrimSpace(replacer.Replace(builder.String()))
}

func clockSeconds(value int) string {
	if value < 0 {
		value = 0
	}
	return fmt.Sprintf("%02d:%02d", value/60, value%60)
}

func transcriptionExtension(contentType string) string {
	switch normalizeMediaType(contentType) {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return ".m4a"
	case "audio/ogg", "audio/opus", "application/ogg":
		return ".ogg"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/webm":
		return ".webm"
	default:
		return ".wav"
	}
}

func truncateText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}
