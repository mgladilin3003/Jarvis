package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/liushuangls/go-anthropic/v2"
)

var regQuestions = []string{
	"Йо! Я Джарвис. Чтобы я стал твоим напарником, давай познакомимся. Как тебя зовут и сколько тебе лет?",
	"На каких языках тебе комфортнее общаться (русский, английский, иврит)?",
	"Чем ты занимаешься по жизни? (Работа, хобби, движ или кодинг?)",
	"Есть ли что-то важное, что мне стоит знать (аллергии, любовь к мемам, особые предпочтения)?",
}

type AgentResponse struct {
	Text      string `json:"text"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Model     string `json:"model"`
}

type Agent struct {
	claudeClient *anthropic.Client
	db           *sql.DB
}

func main() {
	_ = godotenv.Load()
	apiKey, dbUrl := os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("DB_URL")
	db, err := sql.Open("postgres", dbUrl)
	if err != nil {
		log.Fatal(err)
	}

	agent := &Agent{claudeClient: anthropic.NewClient(apiKey), db: db}
	http.HandleFunc("/api/v1/chat", agent.handleChat)
	fmt.Println("🤖 Джарвис готов. Порт :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func (a *Agent) handleChat(w http.ResponseWriter, r *http.Request) {
	// Включаем "щит" от краша
	defer func() {
		if r := recover(); r != nil {
			log.Printf("❌ CRITICAL: Go-agent PANIC caught: %v", r)
		}
	}()

	log.Printf("📥 ЗАПРОС ПРИШЕЛ: %s", r.FormValue("message")) // ЛОГ - если он не появится, значит проблема в сети

	msg := r.FormValue("message")
	uid, _ := strconv.Atoi(r.FormValue("user_id"))
	sessionID, _ := strconv.Atoi(r.FormValue("session_id"))
	uidStr := strconv.Itoa(uid)

	a.db.Exec("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'user', $3)", uid, sessionID, msg)

	memCount := a.getMemCount(uidStr)
	var systemPrompt string

	if memCount < len(regQuestions) {
		systemPrompt = fmt.Sprintf("Ты Джарвис. Идет регистрация. Твой текущий вопрос: '%s'. Если пользователь ответил на него, извлеки факт и сохрани в JSON (в конце ответа): {\"key\": \"...\", \"value\": \"...\", \"cat\": \"profile\"}. Будь дружелюбным, не показывай пользователю JSON-код.", regQuestions[memCount])
	} else if msg == "/new chat" {
		systemPrompt = "Ты Джарвис. Пользователь начал новый чат. Спроси про стиль и цель. Извлеки JSON в конце: {\"key\": \"style/goal\", \"value\": \"...\", \"cat\": \"session\"}."
	} else {
		profile := a.getMemories(uidStr)
		systemPrompt = fmt.Sprintf("Ты Джарвис. Профиль: %s. Твоя задача: отвечать в стиле пользователя, используй IT-сленг. НИКАКОГО MARKDOWN (жирного, курсива, заголовков). Пиши чистый текст.", profile)
	}

	model := "claude-haiku-4-5"
	resp, _ := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model(model),
		MaxTokens: 2048,
		System:    systemPrompt,
		Messages:  []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	fullText := resp.Content[0].GetText()

	// 1. Извлекаем и сохраняем JSON (если есть)
	if jsonStr := a.extractJSON(fullText); jsonStr != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(jsonStr), &m); err == nil {
			a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uidStr, m["key"], m["value"], m["cat"])
		}
	}

	// 2. Чистим ответ от мусора (JSON и Markdown)
	cleanText := a.cleanResponse(fullText)

	a.db.Exec("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'assistant', $3)", uid, sessionID, cleanText)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: cleanText, Model: model})
}

// ХЕЛПЕРЫ
func (a *Agent) getMemCount(uid string) int {
	var count int
	a.db.QueryRow("SELECT COUNT(*) FROM memories WHERE user_id = $1", uid).Scan(&count)
	return count
}

func (a *Agent) getMemories(uid string) string {
	rows, _ := a.db.Query("SELECT fact_key, fact_value FROM memories WHERE user_id = $1", uid)
	var sb strings.Builder
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		sb.WriteString(k + ": " + v + "; ")
	}
	return sb.String()
}

func (a *Agent) extractJSON(text string) string {
	s, e := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if s != -1 && e != -1 {
		return text[s : e+1]
	}
	return ""
}

func (a *Agent) cleanResponse(text string) string {
	// Убираем JSON блок
	jsonPart := a.extractJSON(text)
	text = strings.Replace(text, jsonPart, "", 1)
	// Убираем Markdown
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "#", "")
	text = strings.ReplaceAll(text, "*", "")
	return strings.TrimSpace(text)
}
