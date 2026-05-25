package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/liushuangls/go-anthropic/v2"
)

// ТЕ САМЫЕ ВОПРОСЫ ИЗ ТВОЕГО ПЛАНА (Слово в слово)
var regQuestions = []string{
	"Йо! Я Джарвис, твой новый цифровой бро. Рад, что ты меня подрубил. Чтобы я не был просто железкой, давай познакомимся по-человечески. Как тебя величать и сколько тебе лет (хотя в душе мы все дети, но мне для понимания)?",
	"Живем в мире, где всё перемешано. На каких языках тебе комфортнее штурмовать этот мир? Русский, английский, иврит — пиши, на чем удобно, я пойму всё.",
	"Чем дышишь? Расскажи в паре слов, чем занимаешься по жизни. Ты программист, который сутками фиксит баги, или человек, который предпочитает движ и реальный мир? Это поможет мне понимать, в каком ключе лучше «раскидывать» информацию.",
	"Есть ли что-то, о чем я обязан знать, чтобы не накосячить? Ну, там, аллергия на плохие новости по утрам или особая любовь к мемам?",
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
	sid := r.FormValue("session_id")

	// 1. Запись сообщения пользователя
	var userMessageID int64
	err := a.db.QueryRow("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'user', $3) RETURNING id",
		uid, sid, msg).Scan(&userMessageID)
	if err != nil {
		log.Printf("❌ Ошибка записи сообщения: %v", err)
	}

	memCount := a.getMemCount(uid)
	memories := a.getMemories(uid)

	var systemPrompt string
	if memCount < len(regQuestions) {

		// Собираем список вопросов с их номерами
		var questionsStr string
		for i, q := range regQuestions {
			questionsStr += fmt.Sprintf("%d. %s\n", i+1, q)
		}

		systemPrompt = fmt.Sprintf(`Ты Джарвис, твой новый цифровой бро. Идет процесс знакомства (регистрация).
    
    Список всех вопросов для знакомства:
    %s
    
    Уже известно о пользователе (база фактов): 
    [%s]
    
    ИНСТРУКЦИЯ К ДЕЙСТВИЮ:
    1. Если пользователь ответил на твой вопрос, отреагируй на его ответ в своем фирменном стиле.
    2. В ЭТОМ ЖЕ СООБЩЕНИИ СРАЗУ задай СЛЕДУЮЩИЙ вопрос из списка. Задавай его СЛОВО В СЛОВО, как написано в списке выше! НЕ ПРИДУМЫВАЙ ОТСЕБЯТИНУ!
    3. КРИТИЧЕСКИ ВАЖНО: В самом конце своего ответа (с новой строки) добавь разделитель ---JSON--- и после него напиши валидный JSON: {"key":"short_english_key","value":"ответ пользователя","cat":"profile"}.
    4. ЗАПРЕЩЕНО использовать Markdown-форматирование (никаких звездочек **, решеток #). Пиши только обычным текстом.`, questionsStr, memories)
	} else {
		systemPrompt = fmt.Sprintf(`Ты Джарвис, цифровой напарник. Ты знаешь о пользователе: [%s]. 
        Общайся свободно. ЗАПРЕЩЕНО использовать Markdown (никаких звездочек **, решеток #). Пиши обычным текстом.`, memories)
	}

	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model("claude-haiku-4-5"),
		MaxTokens: 1024,
		System:    systemPrompt,
		Messages:  []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	if err != nil {
		log.Printf("❌ API Error: %v", err)
		http.Error(w, "Claude error", 500)
		return
	}

	// 2. Обновление токенов пользователя (Input)
	if userMessageID != 0 {
		a.db.Exec("UPDATE messages SET tokens_used = $1 WHERE id = $2", resp.Usage.InputTokens, userMessageID)
	}

	fullText := resp.Content[0].GetText()

	// 3. Парсинг данных
	var cleanText string
	if strings.Contains(fullText, "---JSON---") {
		parts := strings.Split(fullText, "---JSON---")
		cleanText = parts[0]

		jsonPart := parts[1]
		s := strings.Index(jsonPart, "{")
		e := strings.LastIndex(jsonPart, "}")
		if s != -1 && e != -1 && e >= s {
			match := jsonPart[s : e+1]
			var m map[string]string
			if err := json.Unmarshal([]byte(match), &m); err == nil {
				a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uid, m["key"], m["value"], m["cat"])
				log.Printf("✅ Записано в БД: %s", m["key"])
			} else {
				log.Printf("❌ Ошибка парсинга JSON: %v", err)
			}
		}
	} else {
		cleanText = fullText
	}

	// 4. Очистка от маркдауна и запись ответа бота
	finalText := a.cleanTrash(cleanText)
	a.db.Exec(`INSERT INTO messages (user_id, session_id, role, content, tokens_used) VALUES ($1, $2, 'assistant', $3, $4)`,
		uid, sid, finalText, resp.Usage.OutputTokens)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: finalText, Model: "claude-haiku-4-5"})
}

func (a *Agent) cleanTrash(text string) string {
	reTags := regexp.MustCompile(`(?i)\[JSON\].*?\[/JSON\]`)
	text = reTags.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "---JSON---", "")

	// ЖЕСТКО удаляем маркдаун
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "*", "")
	text = strings.ReplaceAll(text, "###", "")
	text = strings.ReplaceAll(text, "##", "")
	text = strings.ReplaceAll(text, "#", "")

	return strings.TrimSpace(text)
}

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
