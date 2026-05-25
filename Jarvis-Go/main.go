package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
	Text  string `json:"text"`
	Model string `json:"model"`
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
	msg := r.FormValue("message")
	uid := r.FormValue("user_id")

	// Получаем текущие данные из БД
	memories := a.getMemories(uid)
	memCount := a.getMemCount(uid)

	// СТРОГИЙ ПРОМПТ: Мы явно пишем, что он знает
	systemPrompt := fmt.Sprintf(`Ты Джарвис. Регистрация (%d/%d). 
    Уже известно о пользователе: %s.
    Если пользователь ответил на текущий вопрос, сохрани ответ в JSON [JSON]{"key":"...","value":"...","cat":"profile"}[/JSON].
    Если информации достаточно для перехода к следующему вопросу, не переспрашивай старое.
    Текущий вопрос: %s`,
		memCount, len(regQuestions), memories, regQuestions[memCount])

	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model("claude-haiku-4-5"),
		MaxTokens: 2048,
		System:    systemPrompt,
		Messages:  []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	if err != nil {
		log.Printf("❌ API Error: %v", err)
		http.Error(w, "Claude API error", 500)
		return
	}

	fullText := resp.Content[0].GetText()

	// Парсинг JSON
	jsonStr := a.extractTaggedJSON(fullText)
	if jsonStr != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(jsonStr), &m); err == nil {
			a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uid, m["key"], m["value"], m["cat"])
			log.Printf("✅ Записано: %s", m["key"])
		}
	}

	// Очистка от мусора
	cleanText := a.cleanResponse(fullText)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: cleanText, Model: "claude-haiku-4-5"})
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

func (a *Agent) extractTaggedJSON(text string) string {
	s := strings.Index(text, "[JSON]")
	e := strings.Index(text, "[/JSON]")
	if s != -1 && e != -1 && e > s {
		return text[s+6 : e]
	}
	return ""
}

func (a *Agent) cleanResponse(text string) string {
	s := strings.Index(text, "[JSON]")
	e := strings.Index(text, "[/JSON]")
	if s != -1 && e != -1 {
		text = text[:s] + text[e+7:]
	}
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "#", "")
	text = strings.ReplaceAll(text, "*", "")
	return strings.TrimSpace(text)
}
