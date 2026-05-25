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

var regQuestions = []string{
	"Йо! Я Джарвис, твой цифровой бро. Давай познакомимся. Как тебя величать и сколько тебе лет?",
	"На каких языках тебе комфортнее штурмовать этот мир? Русский, английский, иврит?",
	"Чем занимаешься по жизни? Программист, сутками фиксящий баги, или предпочитаешь реальный мир?",
	"Есть ли что-то, о чем я обязан знать, чтобы не накосячить? (аллергии, предпочтения, ненавидишь утро?)",
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

	chatHistory := a.getChatHistory(uid, sid)
	memories := a.getMemories(uid)
	memCount := a.getMemCount(uid)

	// Считаем кол-во сообщений ДО вставки текущего (чтобы поймать старт сессии)
	var sessionMsgCount int
	a.db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = $1 AND role = 'user'", sid).Scan(&sessionMsgCount)

	var userMessageID int64
	a.db.QueryRow("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'user', $3) RETURNING id",
		uid, sid, msg).Scan(&userMessageID)

	var systemPrompt string

	if memCount < len(regQuestions) {
		// РЕЖИМ 1: РЕГИСТРАЦИЯ
		isLastQuestion := (memCount == len(regQuestions)-1)

		var instruction string
		if isLastQuestion {
			instruction = `ПРАВИЛО ДЛЯ ЭТОГО ОТВЕТА: Это ПОСЛЕДНИЙ вопрос. 
			1. Порадуйся и скажи: "Регистрация успешно завершена! 🎉". 
			2. СРАЗУ ЖЕ (в этом же сообщении) спроси: "В каком тоне будем общаться дальше?" и "Какая цель нашего текущего диалога?".
			3. В конце добавь: ---JSON--- {"key":"...","value":"...","cat":"profile","title":"Знакомство и настройка"}`
		} else {
			instruction = `Отреагируй на ответ и задай СЛЕДУЮЩИЙ ВОПРОС из списка слово в слово. 
			В конце добавь: ---JSON--- {"key":"...","value":"...","cat":"profile"}`
		}

		systemPrompt = fmt.Sprintf(`Ты Джарвис. Идет регистрация. 
		Вопросы: %s
		Известно: [%s]
		
		ГЛАВНЫЕ ПРАВИЛА HTML:
		ИСПОЛЬЗУЙ ТОЛЬКО <b>жирный</b>, <i>курсив</i>, <u>подчеркнутый</u>.
		КАТЕГОРИЧЕСКИ ЗАПРЕЩЕНО использовать <div>, <span>, <br>, style и любые другие теги. НИКАКИХ ЗВЕЗДОЧЕК (**).
		
		%s`, strings.Join(regQuestions, "\n"), memories, instruction)

	} else if sessionMsgCount == 0 {
		// РЕЖИМ 2: СТАРТ НОВОЙ СЕССИИ (/newchat)
		systemPrompt = fmt.Sprintf(`Ты Джарвис. Пользователь: [%s]. Начало нового чата.
		
		ПРАВИЛА:
		1. Поприветствуй пользователя (используй <b>имя</b>). 
		2. Спроси про тон общения и цель этой конкретной сессии.
		3. ИСПОЛЬЗУЙ ТОЛЬКО БАЗОВЫЙ HTML (<b>, <u>). ЗАПРЕЩЕНО использовать <div>, <br>, звездочки (**).
		4. В самом конце добавь: ---JSON--- {"title": "Название темы в 2-3 слова"}`, memories)

	} else {
		// РЕЖИМ 3: РАБОТА
		systemPrompt = fmt.Sprintf(`Ты Джарвис. Память: [%s]. Контекст: %s.
		
		ПРАВИЛА:
		1. Общайся по делу, отвечай на вопросы.
		2. ИСПОЛЬЗУЙ ТОЛЬКО БАЗОВЫЙ HTML (<b>, <u>). ЗАПРЕЩЕНО использовать <div>, <br>, звездочки (**).
		3. Обязательно в конце выведи текущую тему диалога: ---JSON--- {"title": "Суть разговора в 2-3 слова"}`, memories, chatHistory)
	}

	resp, _ := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model("claude-haiku-4-5"),
		MaxTokens: 1024,
		System:    systemPrompt,
		Messages:  []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	if userMessageID != 0 {
		a.db.Exec("UPDATE messages SET tokens_used = $1 WHERE id = $2", resp.Usage.InputTokens, userMessageID)
	}

	fullText := resp.Content[0].GetText()
	cleanText := fullText

	if strings.Contains(fullText, "---JSON---") {
		parts := strings.Split(fullText, "---JSON---")
		cleanText = parts[0]
		var m map[string]string
		re := regexp.MustCompile(`\{.*\}`)

		if err := json.Unmarshal([]byte(re.FindString(parts[1])), &m); err == nil {
			// Сохраняем факты (если есть)
			if key, ok := m["key"]; ok && key != "" {
				a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uid, key, m["value"], m["cat"])
			}

			// ОБНОВЛЯЕМ НАЗВАНИЕ СЕССИИ (Title)
			if title, ok := m["title"]; ok && title != "" {
				// Запрос к таблице chat_sessions. Если у тебя таблица называется иначе - поменяй тут.
				res, err := a.db.Exec("UPDATE chat_sessions SET title = $1 WHERE id = $2", title, sid)
				if err != nil {
					log.Printf("❌ Ошибка обновления названия сессии: %v", err)
				} else {
					affected, _ := res.RowsAffected()
					if affected == 0 {
						log.Printf("⚠️ Сессия %s не найдена в БД для обновления title!", sid)
					} else {
						log.Printf("📝 Название сессии обновлено на: %s", title)
					}
				}
			}
		} else {
			log.Printf("❌ Ошибка парсинга JSON: %v", err)
		}
	}

	finalText := a.cleanTrash(cleanText)
	a.db.Exec(`INSERT INTO messages (user_id, session_id, role, content, tokens_used) VALUES ($1, $2, 'assistant', $3, $4)`,
		uid, sid, finalText, resp.Usage.OutputTokens)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: finalText, Model: "claude-haiku-4-5"})
}

// Усиленная функция очистки мусора
func (a *Agent) cleanTrash(text string) string {
	text = strings.ReplaceAll(text, "---JSON---", "")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "#", "")

	// Жестко вырезаем <div>, <br> и <span>, если Клод попытается их использовать
	reDivs := regexp.MustCompile(`(?i)<div[^>]*>|</div>|<br\s*/?>|<span[^>]*>|</span>`)
	text = reDivs.ReplaceAllString(text, "")

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
	for rows != nil && rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		sb.WriteString(k + ": " + v + "; ")
	}
	return sb.String()
}

func (a *Agent) getChatHistory(uid, sid string) string {
	rows, _ := a.db.Query(`SELECT role, content FROM (SELECT id, role, content FROM messages WHERE user_id = $1 AND session_id = $2 ORDER BY id DESC LIMIT 6) sub ORDER BY id ASC`, uid, sid)
	var sb strings.Builder
	for rows != nil && rows.Next() {
		var r, c string
		rows.Scan(&r, &c)
		sb.WriteString(r + ": " + c + "\n")
	}
	return sb.String()
}
