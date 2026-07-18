package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

var (
	docsHelperURL = envOrDefault("DOCS_HELPER_URL", "http://docs-helper:8080")
	deepseekKey   = os.Getenv("DEEPSEEK_API_KEY")
	deepseekURL   = envOrDefault("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1")
	deepseekModel = envOrDefault("DEEPSEEK_MODEL", "deepseek-v4-flash")
)

func main() {
	log := newLogger(envOrDefault("LOG_LEVEL", "info"))

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Error("TELEGRAM_BOT_TOKEN is required")
		os.Exit(1)
	}
	if deepseekKey == "" {
		log.Error("DEEPSEEK_API_KEY is required")
		os.Exit(1)
	}

	b, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Error("create bot", "error", err)
		os.Exit(1)
	}

	b.Handle("/start", makeStartHandler())
	b.Handle(tele.OnDocument, makeDocHandler(log))
	b.Handle(tele.OnText, makeTextHandler(log))

	log.Info("bot started")
	b.Start()
}

func makeStartHandler() tele.HandlerFunc {
	return func(c tele.Context) error {
		u := c.Sender()
		doGET(fmt.Sprintf("%s/api/v1/documents?telegram_id=%d&username=%s", docsHelperURL, u.ID, u.Username))
		return c.Send("Привет! Я бот для поиска по документам.\n\nЗагрузи PDF или DOCX, затем задай вопрос.")
	}
}

func makeDocHandler(log *slog.Logger) tele.HandlerFunc {
	return func(c tele.Context) error {
		doc := c.Message().Document
		if doc.FileSize > 32<<20 {
			return c.Send("❌ Файл слишком большой. Максимум 32 MB.")
		}
		ext := strings.ToLower(doc.FileName)
		if !strings.HasSuffix(ext, ".pdf") && !strings.HasSuffix(ext, ".docx") {
			return c.Send("❌ Только PDF и DOCX.")
		}

		msg, _ := c.Bot().Send(c.Recipient(), "📥 Документ получен, обрабатываю...")

		file, err := c.Bot().File(&doc.File)
		if err != nil {
			c.Bot().Edit(msg, "❌ Ошибка загрузки.")
			return err
		}
		fileData, err := io.ReadAll(file)
		if err != nil {
			c.Bot().Edit(msg, "❌ Ошибка чтения.")
			return err
		}

		docID, err := uploadFile(c.Sender().ID, c.Sender().Username, doc.FileName, fileData)
		if err != nil {
			c.Bot().Edit(msg, "❌ Ошибка: "+err.Error())
			return err
		}

		deadline := time.Now().Add(5 * time.Minute)
		statuses := map[string]string{
			"extracting": "📖 Извлекаю текст...",
			"chunking":   "🧩 Разбиваю на фрагменты...",
			"embedding":  "🧠 Индексирую...",
			"ready":      "✅ Готово! Задайте вопрос.",
		}
		for {
			if time.Now().After(deadline) {
				c.Bot().Edit(msg, "❌ Превышено время обработки.")
				return nil
			}
			time.Sleep(1 * time.Second)
			ds, err := getDocStatus(docID)
			if err != nil {
				continue
			}
			if text, ok := statuses[ds]; ok {
				c.Bot().Edit(msg, text)
			}
			if ds == "ready" || ds == "error" {
				if ds == "error" {
					c.Bot().Edit(msg, "❌ Ошибка обработки.")
				}
				return nil
			}
		}
	}
}

func makeTextHandler(log *slog.Logger) tele.HandlerFunc {
	return func(c tele.Context) error {
		query := c.Message().Text
		if query == "" {
			return nil
		}
		msg, _ := c.Bot().Send(c.Recipient(), "🔎 Ищу информацию...")

		results, err := searchDocs(c.Sender().ID, query, 10)
		if err != nil || len(results) == 0 {
			c.Bot().Edit(msg, "В загруженных документах не найдено информации.")
			return nil
		}

		c.Bot().Edit(msg, "🤔 Анализирую...")
		answer, sources := askDeepSeek(query, results)

		var sb strings.Builder
		sb.WriteString(answer)
		sb.WriteString("\n\n📎 *Источники:*")
		for _, s := range sources {
			sb.WriteString(fmt.Sprintf("\n• %s, стр. %d", s.Doc, s.Page))
		}
		c.Bot().Edit(msg, sb.String(), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
		return nil
	}
}

// --- docs-helper API ---

func uploadFile(telegramID int64, username, filename string, data []byte) (int64, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", filename)
	part.Write(data)
	w.Close()

	url := fmt.Sprintf("%s/api/v1/documents?telegram_id=%d&username=%s", docsHelperURL, telegramID, username)
	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("upload http %d: %s", resp.StatusCode, string(body))
	}

	var doc struct{ ID int64 `json:"id"` }
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return 0, fmt.Errorf("upload decode: %w", err)
	}
	return doc.ID, nil
}

func getDocStatus(docID int64) (string, error) {
	var doc struct{ Status string `json:"status"` }
	err := doJSON("GET", fmt.Sprintf("%s/api/v1/documents/%d", docsHelperURL, docID), nil, &doc)
	return doc.Status, err
}

type searchResult struct {
	Chunk   chunk   `json:"chunk"`
	Score   float32 `json:"score"`
	DocName string  `json:"doc_name"`
}
type chunk struct {
	Text    string `json:"text"`
	PageNum int    `json:"page_num"`
	DocID   int64  `json:"doc_id"`
}

func searchDocs(telegramID int64, query string, limit int) ([]searchResult, error) {
	var resp struct {
		Results []searchResult `json:"results"`
	}
	err := doJSON("POST", docsHelperURL+"/api/v1/search", map[string]any{
		"telegram_id": telegramID,
		"query":       query,
		"limit":       limit,
	}, &resp)
	return resp.Results, err
}

// --- DeepSeek ---

type sourceInfo struct {
	Doc  string
	Page int
}

func askDeepSeek(query string, results []searchResult) (string, []sourceInfo) {
	var ctx strings.Builder
	sources := make([]sourceInfo, 0, len(results))
	for i, r := range results {
		name := r.DocName
		if name == "" {
			name = fmt.Sprintf("документ-%d", r.Chunk.DocID)
		}
		sources = append(sources, sourceInfo{Doc: name, Page: r.Chunk.PageNum})
		ctx.WriteString(fmt.Sprintf("\n[Источник %d: %s, стр. %d]\n%s\n", i+1, name, r.Chunk.PageNum, r.Chunk.Text))
	}

	prompt := fmt.Sprintf(`Ты — ассистент, отвечающий ТОЛЬКО на основе предоставленных документов.
Если информация противоречива — укажи все варианты с источниками.
Если информации недостаточно — скажи честно.

Документы:%s

Вопрос: %s

Ответ:`, ctx.String(), query)

	body := map[string]any{
		"model": deepseekModel,
		"messages": []map[string]string{
			{"role": "system", "content": "Ты отвечаешь только на основе предоставленных фрагментов. Будь точен, указывай источники. При противоречиях приводи все варианты."},
			{"role": "user", "content": prompt},
		},
	}

	var resp struct {
		Choices []struct {
			Message struct{ Content string `json:"content"` } `json:"message"`
		} `json:"choices"`
	}
	if err := doJSONWithAuth("POST", deepseekURL+"/chat/completions", body, &resp, deepseekKey); err != nil {
		return "❌ Ошибка генерации ответа.", sources
	}
	if len(resp.Choices) == 0 {
		return "Не удалось сформировать ответ.", sources
	}
	return resp.Choices[0].Message.Content, sources
}

// --- HTTP helpers ---

var httpClient = &http.Client{Timeout: 30 * time.Second}

func doGET(url string) {
	resp, _ := httpClient.Get(url)
	if resp != nil {
		resp.Body.Close()
	}
}

func doJSON(method, url string, body, into any) error {
	return doJSONWithAuth(method, url, body, into, "")
}

func doJSONWithAuth(method, url string, body, into any, apiKey string) error {
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, url, r)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(bodyBytes))
	}
	if into != nil {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
