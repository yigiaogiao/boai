package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"boai/internal/api"
	"boai/internal/service"
	"boai/internal/store"
)

type aiRig struct {
	handler http.Handler
	store   *store.Store
	service *service.Service
	feedURL string
	fakeAI  string
	logs    *bytes.Buffer
	uploads atomic.Int64
	texts   atomic.Int64

	promptMu   sync.Mutex
	lastPrompt string
}

func newAIRig(t *testing.T) *aiRig {
	t.Helper()
	rig := &aiRig{logs: &bytes.Buffer{}}
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
			_, _ = fmt.Fprintf(w, feedTemplate, r.Host)
		case "/episode.wav":
			http.ServeFile(w, r, fixturePath("episode.wav"))
		case "/episode.vtt":
			data, err := os.ReadFile(fixturePath("episode.vtt"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(feedServer.Close)
	rig.feedURL = feedServer.URL

	fakeAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer fake-") {
			http.Error(w, `{"error":{"message":"missing key"}}`, http.StatusUnauthorized)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/fail/") {
			http.Error(w, `{"error":{"message":"fake provider failure"}}`, http.StatusBadGateway)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/slow/") {
			time.Sleep(400 * time.Millisecond)
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/slow")
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			rig.texts.Add(1)
			var request struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			user := ""
			if len(request.Messages) > 0 {
				user = request.Messages[len(request.Messages)-1].Content
			}
			rig.promptMu.Lock()
			rig.lastPrompt = user
			rig.promptMu.Unlock()
			content := fakeTextContent(user)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			})
		case "/v1/audio/transcriptions":
			rig.uploads.Add(1)
			if err := r.ParseMultipartForm(16 << 20); err != nil {
				http.Error(w, "bad multipart", http.StatusBadRequest)
				return
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "missing file", http.StatusBadRequest)
				return
			}
			raw, _ := io.ReadAll(file)
			file.Close()
			if len(raw) < 44 {
				http.Error(w, "empty audio", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text":     "云端转写的测试内容。",
				"language": "zh",
				"segments": []map[string]any{
					{"start": 0.0, "end": 8.0, "text": "云端转写的测试内容。"},
					{"start": 9.0, "end": 20.0, "text": "没有来源逐字稿时才会上传音频。"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeAI.Close)
	rig.fakeAI = fakeAI.URL

	st, err := store.Open(filepath.Join(t.TempDir(), "ai-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rig.store = st
	rig.service = service.New(st, service.Options{
		UpdateInterval:  time.Hour,
		HTTPTimeout:     5 * time.Second,
		AllowPrivate:    true,
		AllowInsecureAI: true,
		Secret:          "stage-b-test-secret",
	})
	rig.handler = api.New(rig.service, api.Options{
		WebDir:          t.TempDir(),
		UserHeader:      "X-HC-User-ID",
		TrustUserHeader: true,
		Version:         "test-version",
		Logger:          log.New(rig.logs, "", 0),
	}).Handler()
	return rig
}

func fakeTextContent(user string) string {
	switch {
	case strings.Contains(user, "只回复 OK"):
		return "OK"
	case strings.Contains(user, "目标语言："):
		return `{"translations":[{"segment":0,"text":"译文零"},{"segment":1,"text":"译文一"},{"segment":2,"text":"译文二"},{"segment":3,"text":"译文三"}]}`
	case strings.Contains(user, "用户问题："):
		if strings.Contains(user, "火星") {
			return `{"answer":"逐字稿没有提到火星，证据不足。","grounded":false,"citations":[]}`
		}
		return `{"answer":"这集强调倾听不是等待轮到自己说话。","grounded":true,"citations":[{"segment":2,"quote":"倾听不是等着轮到自己说话"}]}`
	case strings.Contains(user, "精读标题"):
		return `{"title":"倾听的精读","summary":"围绕倾听展开。","sections":[{"heading":"倾听的定义","body":"允许对方改变方向。","citations":[{"segment":2,"quote":"倾听不是等着轮到自己说话"}]}],"takeaways":[{"text":"先确认听到什么。","citations":[{"segment":3,"quote":"先确认自己听到了什么"}]}]}`
	case strings.Contains(user, "导图标题"):
		return `{"title":"倾听导图","branches":[{"title":"方法","children":[{"title":"允许改变方向","citations":[{"segment":2,"quote":"倾听不是等着轮到自己说话"}]}]}]}`
	case strings.Contains(user, "总览"):
		return `{"overview":"这集讨论如何真正倾听。","chapters":[{"segment":2,"title":"倾听的定义"}],"points":[{"text":"倾听允许对方改变方向。","citations":[{"segment":2,"quote":"倾听不是等着轮到自己说话"}]}],"quotes":[{"segment":2,"quote":"倾听不是等着轮到自己说话"}]}`
	default:
		return `{"overview":"fake"}`
	}
}

func saveAIConfig(t *testing.T, rig *aiRig, owner, kind, base, model, key string) service.AIProviderStatus {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"provider": "openai-compatible", "base_url": base, "model": model, "api_key": key,
	})
	recorder := do(t, rig.handler, http.MethodPut, "/api/ai/settings/"+kind, owner, body, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save %s config status = %d body=%s", kind, recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), key) {
		t.Fatal("save response leaked the API key")
	}
	return decode[struct {
		Config service.AIProviderStatus `json:"config"`
	}](t, recorder, http.StatusOK).Config
}

func waitTask(t *testing.T, rig *aiRig, owner, taskID string) service.TaskView {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		recorder := do(t, rig.handler, http.MethodGet, "/api/tasks/"+taskID, owner, nil, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("get task status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		task := decode[struct {
			Task service.TaskView `json:"task"`
		}](t, recorder, http.StatusOK).Task
		if task.Status == "completed" || task.Status == "failed" {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s did not finish", taskID)
	return service.TaskView{}
}

func episodeAI(t *testing.T, rig *aiRig, owner string, episodeID int64) service.EpisodeAI {
	t.Helper()
	return decode[service.EpisodeAI](t,
		do(t, rig.handler, http.MethodGet, fmt.Sprintf("/api/episodes/%d/ai", episodeID), owner, nil, nil),
		http.StatusOK)
}

func TestAIFakeProviderLifecycleAndIsolation(t *testing.T) {
	rig := newAIRig(t)
	owner := "ai-user-a"
	body, _ := json.Marshal(map[string]any{"feed_url": rig.feedURL + "/feed.xml"})
	subscription := decode[struct {
		Feed store.Feed `json:"feed"`
	}](t, do(t, rig.handler, http.MethodPost, "/api/feeds", owner, body, nil), http.StatusCreated)
	if subscription.Feed.ID == 0 {
		t.Fatal("subscription failed")
	}
	episodes := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, rig.handler, http.MethodGet, "/api/episodes", owner, nil, nil), http.StatusOK).Episodes
	var withTranscript, withoutTranscript store.Episode
	for _, episode := range episodes {
		if episode.TranscriptRefsAvailable {
			withTranscript = episode
		} else {
			withoutTranscript = episode
		}
	}
	if withTranscript.ID == 0 || withoutTranscript.ID == 0 {
		t.Fatalf("episodes = %#v", episodes)
	}

	status := decode[struct {
		AI service.AIStatusView `json:"ai"`
	}](t, do(t, rig.handler, http.MethodGet, "/api/status", owner, nil, nil), http.StatusOK).AI
	if status.Configured || status.Text.Configured || status.Speech.Configured || !status.SecretReady {
		t.Fatalf("initial AI status = %#v", status)
	}
	if recorder := do(t, rig.handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/insights/summary", withTranscript.ID), owner, nil, nil); recorder.Code != http.StatusConflict {
		t.Fatalf("generate without key status = %d, want 409", recorder.Code)
	}

	textConfig := saveAIConfig(t, rig, owner, "text", rig.fakeAI+"/v1", "fake-text", "fake-text-key-123")
	if !textConfig.KeySet || textConfig.BaseURL != rig.fakeAI+"/v1" {
		t.Fatalf("text config = %#v", textConfig)
	}
	status = decode[struct {
		AI service.AIStatusView `json:"ai"`
	}](t, do(t, rig.handler, http.MethodGet, "/api/ai/settings", owner, nil, nil), http.StatusOK).AI
	if !status.Text.Configured || status.Speech.Configured || !status.Configured {
		t.Fatalf("independent config status = %#v", status)
	}
	testText := decode[service.AITestResult](t,
		do(t, rig.handler, http.MethodPost, "/api/ai/settings/text/test", owner, nil, nil), http.StatusOK)
	if !testText.OK {
		t.Fatalf("text test = %#v", testText)
	}

	if recorder := do(t, rig.handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/transcription", withoutTranscript.ID), owner, nil, nil); recorder.Code != http.StatusConflict {
		t.Fatalf("transcription without speech config status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// Source-provided transcript must be reused and must not cause audio upload.
	if recorder := do(t, rig.handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/transcript", withTranscript.ID), owner, nil, nil); recorder.Code != http.StatusOK {
		t.Fatalf("fetch source transcript status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	transcript := decode[struct {
		Transcript store.Transcript `json:"transcript"`
	}](t, do(t, rig.handler, http.MethodGet,
		fmt.Sprintf("/api/episodes/%d/transcript", withTranscript.ID), owner, nil, nil), http.StatusOK).Transcript
	if len(transcript.Segments) != 4 {
		t.Fatalf("source transcript segments = %d", len(transcript.Segments))
	}

	summaryTask := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/insights/summary", withTranscript.ID), owner, nil, nil), http.StatusAccepted).Task
	if summaryTask.Sends != "text" || !strings.Contains(summaryTask.CostNotice, "文本模型") {
		t.Fatalf("summary disclosure = %#v", summaryTask)
	}
	summaryTask = waitTask(t, rig, owner, summaryTask.ID)
	if summaryTask.Status != "completed" {
		t.Fatalf("summary task = %#v", summaryTask)
	}
	aiResult := episodeAI(t, rig, owner, withTranscript.ID)
	if len(aiResult.Insights) != 1 || aiResult.Insights[0].Kind != "summary" {
		t.Fatalf("summary insights = %#v", aiResult.Insights)
	}
	var summary struct {
		Overview string `json:"overview"`
		Chapters []struct {
			Start float64 `json:"start"`
			Label string  `json:"label"`
		} `json:"chapters"`
		Points []struct {
			Citations []service.CitationView `json:"citations"`
		} `json:"points"`
	}
	if err := json.Unmarshal(aiResult.Insights[0].Payload, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Overview == "" || len(summary.Chapters) != 1 || summary.Chapters[0].Start != 21 ||
		len(summary.Points) != 1 || len(summary.Points[0].Citations) != 1 ||
		summary.Points[0].Citations[0].Start != 21 {
		t.Fatalf("summary payload = %s", aiResult.Insights[0].Payload)
	}
	rig.promptMu.Lock()
	prompt := rig.lastPrompt
	rig.promptMu.Unlock()
	if !strings.Contains(prompt, "第一集：倾听") || strings.Contains(prompt, "第二集：选择") {
		t.Fatalf("prompt was not scoped to the current episode: %s", prompt)
	}

	// Translation uses source text, never audio.
	beforeUploads := rig.uploads.Load()
	translationTask := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost, fmt.Sprintf("/api/episodes/%d/translation", withTranscript.ID),
		owner, []byte(`{"target_language":"英文"}`), nil), http.StatusAccepted).Task
	translationTask = waitTask(t, rig, owner, translationTask.ID)
	if translationTask.Status != "completed" {
		t.Fatalf("translation task = %#v", translationTask)
	}
	if rig.uploads.Load() != beforeUploads {
		t.Fatal("translation uploaded audio")
	}
	translated := decode[struct {
		Transcript store.Transcript `json:"transcript"`
	}](t, do(t, rig.handler, http.MethodGet,
		fmt.Sprintf("/api/episodes/%d/transcript", withTranscript.ID), owner, nil, nil), http.StatusOK).Transcript
	if translated.TranslatedLanguage != "英文" || translated.Segments[0].Translation == "" ||
		translated.Segments[0].Text == "" {
		t.Fatalf("translated transcript = %#v", translated)
	}

	// Ask is scoped to this episode and cites a real segment.
	askTask := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost, fmt.Sprintf("/api/episodes/%d/ask", withTranscript.ID),
		owner, []byte(`{"question":"这集对倾听的定义是什么？"}`), nil), http.StatusAccepted).Task
	askTask = waitTask(t, rig, owner, askTask.ID)
	if askTask.Status != "completed" {
		t.Fatalf("ask task = %#v", askTask)
	}
	aiResult = episodeAI(t, rig, owner, withTranscript.ID)
	if len(aiResult.Questions) != 1 || !aiResult.Questions[0].Grounded {
		t.Fatalf("questions = %#v", aiResult.Questions)
	}
	var citations []service.CitationView
	if err := json.Unmarshal(aiResult.Questions[0].Citations, &citations); err != nil {
		t.Fatal(err)
	}
	if len(citations) != 1 || citations[0].Segment != 2 || citations[0].Quote == "" {
		t.Fatalf("citations = %#v", citations)
	}

	// A question with no support is explicit rather than fabricated.
	unsupported := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost, fmt.Sprintf("/api/episodes/%d/ask", withTranscript.ID),
		owner, []byte(`{"question":"火星上怎么种土豆？"}`), nil), http.StatusAccepted).Task
	unsupported = waitTask(t, rig, owner, unsupported.ID)
	if unsupported.Status != "completed" {
		t.Fatalf("unsupported task = %#v", unsupported)
	}
	aiResult = episodeAI(t, rig, owner, withTranscript.ID)
	if aiResult.Questions[0].Grounded || !strings.Contains(aiResult.Questions[0].Answer, "证据不足") {
		t.Fatalf("unsupported answer = %#v", aiResult.Questions[0])
	}

	// Configure speech independently; connection test uploads only test audio.
	saveAIConfig(t, rig, owner, "speech", rig.fakeAI+"/v1", "fake-speech", "fake-speech-key-456")
	if rig.uploads.Load() != beforeUploads {
		t.Fatal("speech config save uploaded audio")
	}
	speechTest := decode[service.AITestResult](t,
		do(t, rig.handler, http.MethodPost, "/api/ai/settings/speech/test", owner, nil, nil), http.StatusOK)
	if !speechTest.OK || rig.uploads.Load() != beforeUploads+1 {
		t.Fatalf("speech test = %#v uploads=%d", speechTest, rig.uploads.Load())
	}
	transcribeTask := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/transcription", withoutTranscript.ID), owner, nil, nil), http.StatusAccepted).Task
	if transcribeTask.Sends != "audio" || !strings.Contains(transcribeTask.CostNotice, "音频") {
		t.Fatalf("transcription disclosure = %#v", transcribeTask)
	}
	transcribeTask = waitTask(t, rig, owner, transcribeTask.ID)
	if transcribeTask.Status != "completed" {
		t.Fatalf("transcription task = %#v", transcribeTask)
	}
	if rig.uploads.Load() != beforeUploads+2 {
		t.Fatalf("transcription uploads = %d, want %d", rig.uploads.Load(), beforeUploads+2)
	}
	cloudTranscript := decode[struct {
		Transcript store.Transcript `json:"transcript"`
	}](t, do(t, rig.handler, http.MethodGet,
		fmt.Sprintf("/api/episodes/%d/transcript", withoutTranscript.ID), owner, nil, nil), http.StatusOK).Transcript
	if len(cloudTranscript.Segments) != 2 || cloudTranscript.Source != "AI 转写" || !cloudTranscript.Timed {
		t.Fatalf("cloud transcript = %#v", cloudTranscript)
	}
	if recorder := do(t, rig.handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/transcription", withoutTranscript.ID), owner, nil, nil); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second transcription status = %d, want 422", recorder.Code)
	}

	// Two-user isolation: B sees no config, results, key or task.
	statusB := decode[struct {
		AI service.AIStatusView `json:"ai"`
	}](t, do(t, rig.handler, http.MethodGet, "/api/ai/settings", "ai-user-b", nil, nil), http.StatusOK).AI
	if statusB.Configured {
		t.Fatalf("user B status = %#v", statusB)
	}
	if recorder := do(t, rig.handler, http.MethodGet,
		fmt.Sprintf("/api/episodes/%d/ai", withTranscript.ID), "ai-user-b", nil, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign AI result status = %d, want 404", recorder.Code)
	}
	if recorder := do(t, rig.handler, http.MethodGet, "/api/tasks/"+summaryTask.ID, "ai-user-b", nil, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign task status = %d, want 404", recorder.Code)
	}

	// Deleting the key keeps generated history readable.
	if recorder := do(t, rig.handler, http.MethodDelete, "/api/ai/settings/text", owner, nil, nil); recorder.Code != http.StatusOK {
		t.Fatalf("delete text status = %d", recorder.Code)
	}
	status = decode[struct {
		AI service.AIStatusView `json:"ai"`
	}](t, do(t, rig.handler, http.MethodGet, "/api/ai/settings", owner, nil, nil), http.StatusOK).AI
	if status.Text.Configured || !status.Speech.Configured {
		t.Fatalf("config isolation after delete: %#v", status)
	}
	aiResult = episodeAI(t, rig, owner, withTranscript.ID)
	if len(aiResult.Insights) != 1 || len(aiResult.Questions) != 2 {
		t.Fatalf("history lost after key delete: %#v", aiResult)
	}

	// The fake key is absent from responses and logs.
	for _, value := range []string{rig.logs.String(), status.Message} {
		if strings.Contains(value, "fake-text-key-123") || strings.Contains(value, "fake-speech-key-456") {
			t.Fatalf("key leaked: %s", value)
		}
	}
	var encrypted string
	if err := rig.store.DB().QueryRow(`SELECT api_key_enc FROM ai_configs WHERE owner = ? AND kind = 'speech'`, owner).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == "" || strings.Contains(encrypted, "fake-speech-key-456") {
		t.Fatalf("stored key is not encrypted: %q", encrypted)
	}
}

func TestAITaskDeduplicationFailureRetryAndRestartRecovery(t *testing.T) {
	rig := newAIRig(t)
	owner := "ai-task-user"
	body, _ := json.Marshal(map[string]any{"feed_url": rig.feedURL + "/feed.xml"})
	_ = decode[struct {
		Feed store.Feed `json:"feed"`
	}](t, do(t, rig.handler, http.MethodPost, "/api/feeds", owner, body, nil), http.StatusCreated)
	episodes := decode[struct {
		Episodes []store.Episode `json:"episodes"`
	}](t, do(t, rig.handler, http.MethodGet, "/api/episodes", owner, nil, nil), http.StatusOK).Episodes
	var episode store.Episode
	for _, item := range episodes {
		if item.TranscriptRefsAvailable {
			episode = item
		}
	}
	if recorder := do(t, rig.handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/transcript", episode.ID), owner, nil, nil); recorder.Code != http.StatusOK {
		t.Fatalf("transcript fetch = %d", recorder.Code)
	}
	saveAIConfig(t, rig, owner, "text", rig.fakeAI+"/slow/v1", "fake-text", "fake-slow-key")

	// Two immediate clicks return the same active task, avoiding duplicate
	// provider charges.
	path := fmt.Sprintf("/api/episodes/%d/insights/summary", episode.ID)
	first := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost, path, owner, nil, nil), http.StatusAccepted).Task
	second := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost, path, owner, nil, nil), http.StatusAccepted).Task
	if first.ID != second.ID {
		t.Fatalf("duplicate clicks created tasks %s and %s", first.ID, second.ID)
	}
	if done := waitTask(t, rig, owner, first.ID); done.Status != "completed" {
		t.Fatalf("slow task = %#v", done)
	}

	// Point at a failing compatible endpoint, then retry after fixing it.
	body, _ = json.Marshal(map[string]string{
		"provider": "openai-compatible", "base_url": rig.fakeAI + "/fail", "model": "fake-text", "api_key": "",
	})
	if recorder := do(t, rig.handler, http.MethodPut, "/api/ai/settings/text", owner, body, nil); recorder.Code != http.StatusOK {
		t.Fatalf("update failing config = %d %s", recorder.Code, recorder.Body.String())
	}
	failed := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost,
		fmt.Sprintf("/api/episodes/%d/insights/analysis", episode.ID), owner, nil, nil), http.StatusAccepted).Task
	failed = waitTask(t, rig, owner, failed.ID)
	if failed.Status != "failed" || failed.Error == "" {
		t.Fatalf("failed task = %#v", failed)
	}
	failedTest := decode[service.AITestResult](t,
		do(t, rig.handler, http.MethodPost, "/api/ai/settings/text/test", owner, nil, nil), http.StatusOK)
	if failedTest.OK || failedTest.Message == "" {
		t.Fatalf("failing connection test = %#v", failedTest)
	}
	body, _ = json.Marshal(map[string]string{
		"provider": "openai-compatible", "base_url": rig.fakeAI + "/v1", "model": "fake-text", "api_key": "",
	})
	if recorder := do(t, rig.handler, http.MethodPut, "/api/ai/settings/text", owner, body, nil); recorder.Code != http.StatusOK {
		t.Fatalf("restore config = %d %s", recorder.Code, recorder.Body.String())
	}
	retried := decode[struct {
		Task service.TaskView `json:"task"`
	}](t, do(t, rig.handler, http.MethodPost, "/api/tasks/"+failed.ID+"/retry", owner, nil, nil), http.StatusAccepted).Task
	if retried.ID != failed.ID || retried.Status != "queued" {
		t.Fatalf("retry task = %#v", retried)
	}
	if done := waitTask(t, rig, owner, failed.ID); done.Status != "completed" || done.Attempts < 2 {
		t.Fatalf("retried task = %#v", done)
	}

	// Simulate a process restart with queued/processing rows in SQLite.
	queued, err := rig.store.CreateTask(t.Context(), store.Task{
		ID: "restart-task", Owner: owner, EpisodeID: episode.ID, Kind: "ask", Status: "queued",
		Payload: `{"question":"restart?"}`, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	processing, err := rig.store.CreateTask(t.Context(), store.Task{
		ID: "processing-task", Owner: owner, EpisodeID: episode.ID, Kind: "map", Status: "processing",
		Payload: `{}`, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = service.New(rig.store, service.Options{
		UpdateInterval: time.Hour, HTTPTimeout: 5 * time.Second,
		AllowPrivate: true, AllowInsecureAI: true, Secret: "stage-b-test-secret",
	})
	for _, id := range []string{queued.ID, processing.ID} {
		task, err := rig.store.GetTask(t.Context(), owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != "failed" || !strings.Contains(task.Error, "重启") {
			t.Fatalf("recovered task %s = %#v", id, task)
		}
	}
}
