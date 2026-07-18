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
	b.Handle("/documents", makeDocsListHandler(log))
	b.Handle(tele.OnDocument, makeDocHandler(log))
	b.Handle(tele.OnText, makeTextHandler(log))
	b.Handle(tele.OnCallback, makeCallbackHandler(log, b))

	log.Info("bot started")
	b.Start()
}

func makeStartHandler() tele.HandlerFunc {
	return func(c tele.Context) error {
		u := c.Sender()
		doGET(fmt.Sprintf("%s/api/v1/documents?telegram_id=%d&username=%s", docsHelperURL, u.ID, u.Username))

		menu := &tele.ReplyMarkup{ResizeKeyboard: true}
		menu.Reply(menu.Row(menu.Text("📚 Мои документы")))

		return c.Send("Привет! Я бот для поиска по документам.\n\nЗагрузи PDF или DOCX, затем задай вопрос.", menu)
	}
}

func makeDocsListHandler(log *slog.Logger) tele.HandlerFunc {
	return func(c tele.Context) error {
		return showDocsList(c)
	}
}

func showDocsList(c tele.Context) error {
	docs, err := listDocs(c.Sender().ID)
	if err != nil || len(docs) == 0 {
		return c.Send("У вас пока нет загруженных документов.")
	}

	var lines []string
	selector := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, d := range docs {
		status := "✅"
		if d.Status == "error" {
			status = "❌"
		} else if d.Status != "ready" {
			status = "⏳"
		}
		lines = append(lines, fmt.Sprintf("%s %s", status, d.Name))
		btn := selector.Data("🗑 "+d.Name, "del", fmt.Sprintf("%d", d.ID))
		rows = append(rows, selector.Row(btn))
	}
	selector.Inline(rows...)

	return c.Send("📚 Ваши документы:\n\n"+strings.Join(lines, "\n"), selector)
}

func makeCallbackHandler(log *slog.Logger, bot *tele.Bot) tele.HandlerFunc {
	return func(c tele.Context) error {
		data := c.Data()

		// Confirm phase: user tapped delete, show confirm/cancel
		if strings.HasPrefix(data, "del|") {
			docID := strings.TrimPrefix(data, "del|")
			selector := &tele.ReplyMarkup{}
			selector.Inline(
				selector.Row(
					selector.Data("✅ Да, удалить", "confirm|"+docID),
					selector.Data("❌ Отмена", "cancel"),
				),
			)
			return c.Edit("Удалить документ?", selector)
		}

		// Confirm: execute delete
		if strings.HasPrefix(data, "confirm|") {
			docID := strings.TrimPrefix(data, "confirm|")
			user := c.Sender()
			url := fmt.Sprintf("%s/api/v1/documents/%s?telegram_id=%d", docsHelperURL, docID, user.ID)
			req, _ := http.NewRequest("DELETE", url, nil)
			resp, err := httpClient.Do(req)
			if err != nil || resp.StatusCode >= 400 {
				return c.Respond(&tele.CallbackResponse{Text: "❌ Ошибка"})
			}
			resp.Body.Close()

			// Refresh list
			docs, _ := listDocs(user.ID)
			if len(docs) == 0 {
				bot.Edit(c.Message(), "Документы удалены.")
				return c.Respond(&tele.CallbackResponse{Text: "✅ Удалено"})
			}
			refreshDocsMessage(bot, c.Message(), docs)
			return c.Respond(&tele.CallbackResponse{Text: "✅ Удалено"})
		}

		// Cancel
		if data == "cancel" {
			docs, _ := listDocs(c.Sender().ID)
			if len(docs) == 0 {
				bot.Edit(c.Message(), "Документы удалены.")
			} else {
				refreshDocsMessage(bot, c.Message(), docs)
			}
			return c.Respond()
		}

		return c.Respond()
	}
}

func refreshDocsMessage(bot *tele.Bot, msg *tele.Message, docs []docInfo) {
	var lines []string
	selector := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, d := range docs {
		status := "✅"
		if d.Status == "error" {
			status = "❌"
		} else if d.Status != "ready" {
			status = "⏳"
		}
		lines = append(lines, fmt.Sprintf("%s %s", status, d.Name))
		btn := selector.Data("🗑 "+d.Name, "del", fmt.Sprintf("%d", d.ID))
		rows = append(rows, selector.Row(btn))
	}
	selector.Inline(rows...)
	bot.Edit(msg, "📚 Ваши документы:\n\n"+strings.Join(lines, "\n"), selector)
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
		if query == "📚 Мои документы" {
			return showDocsList(c)
		}

		msg, _ := c.Bot().Send(c.Recipient(), "🔎 Ищу информацию...")

		log.Info("searching", "query", query)
		results, err := searchDocs(c.Sender().ID, query, 20)
		log.Info("search done", "results", len(results), "error", err)
		if err != nil || len(results) == 0 {
			c.Bot().Edit(msg, "В загруженных документах не найдено информации.")
			return nil
		}

		log.Info("asking deepseek", "results", len(results))
		c.Bot().Edit(msg, "🤔 Анализирую...")
		answer, sources := askDeepSeek(log, query, results)

		log.Info("deepseek done", "answer_len", len(answer))
		var sb strings.Builder
		sb.WriteString(answer)
		if len(sources) > 0 {
			sb.WriteString("\n\n📎 Источники:")
			for _, s := range sources {
				sb.WriteString(fmt.Sprintf("\n• %s, %s", s.Doc, s.Ref))
			}
		}
		if _, err := c.Bot().Edit(msg, sb.String()); err != nil {
			log.Error("edit failed, fallback send", "error", err)
			c.Bot().Send(c.Recipient(), sb.String())
		}
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

	resp, err := httpClient.Do(req)
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

func listDocs(telegramID int64) ([]docInfo, error) {
	var docs []docInfo
	err := doJSON("GET", fmt.Sprintf("%s/api/v1/documents?telegram_id=%d", docsHelperURL, telegramID), nil, &docs)
	return docs, err
}

type docInfo struct {
	ID     int64  `json:"id"`
	Name   string `json:"original_name"`
	Status string `json:"status"`
}

type searchResult struct {
	Chunk   chunk   `json:"chunk"`
	Score   float32 `json:"score"`
	DocName string  `json:"doc_name"`
}
type chunk struct {
	Text    string `json:"text"`
	PageNum int    `json:"page_num"`
	Section string `json:"section"`
	DocID   int64  `json:"doc_id"`
}

func (ch chunk) sourceRef() string {
	if ch.Section != "" {
		return ch.Section
	}
	return fmt.Sprintf("стр. %d", ch.PageNum)
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
	Doc string
	Ref string
}

func askDeepSeek(log *slog.Logger, query string, results []searchResult) (string, []sourceInfo) {
	var ctx strings.Builder
	seen := make(map[string]bool)
	var sources []sourceInfo

	for i, r := range results {
		name := r.DocName
		if name == "" {
			name = fmt.Sprintf("документ-%d", r.Chunk.DocID)
		}
		ref := r.Chunk.sourceRef()
		ctx.WriteString(fmt.Sprintf("\n[Источник %d: %s, %s]\n%s\n", i+1, name, ref, r.Chunk.Text))

		key := fmt.Sprintf("%s|%s", name, ref)
		if seen[key] {
			continue
		}
		seen[key] = true
		sources = append(sources, sourceInfo{Doc: name, Ref: ref})
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
		log.Error("deepseek", "error", err)
		return "❌ Ошибка генерации ответа. Попробуйте позже.", sources
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
		fmt.Fprintf(os.Stderr, "http error %s %s: %d %s\n", method, url, resp.StatusCode, string(bodyBytes))
		return fmt.Errorf("http %d", resp.StatusCode)
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
