package main // Говорим, что это главный файл, который можно запускать

import (
	"context"  // Нужен для управления временем выполнения запроса
	"fmt"      // Библиотека для печати текста в консоль (сокращение от Format)
	"log"      // Для красивой записи ошибок с указанием времени
	"net/http" // Самый важный инструмент — позволяет нашей программе стать сервером
	"os"       // Позволяет читать переменные из системы (наш .env файл)

	"github.com/joho/godotenv"               // Подключенная библиотека для чтения .env
	"github.com/liushuangls/go-anthropic/v2" // Библиотека для общения с Claude
)

// Создаем "чертеж" (структуру) нашего Агента.
// У него внутри будет жить готовый клиент для связи с Claude.
type Agent struct {
	claudeClient *anthropic.Client
}

func main() {
	// 1. Пытаемся прочитать файл .env, который лежит рядом
	if err := godotenv.Load(); err != nil {
		log.Println("Файл .env не найден, но ничего, попробуем прочитать системные переменные")
	}

	// 2. Достаем из секретного кармана ключ от Claude
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("Критическая ошибка! Ты забыл положить ANTHROPIC_API_KEY в файл .env")
	}

	// 3. Создаем Агента и вкладываем в него созданный клиент Claude
	agent := &Agent{
		claudeClient: anthropic.NewClient(apiKey),
	}

	// 4. Говорим нашему серверу: "Если к тебе постучатся по адресу /api/v1/chat — позови функцию handleChat"
	http.HandleFunc("/api/v1/chat", agent.handleChat)

	// 5. Включаем сервер на порту 8081
	fmt.Println("🤖 Go-агент Джарвиса включил уши на порту :8081 и ждет Java...")

	// Эта строчка запускает бесконечный цикл. Наш Повар встал у плиты.
	log.Fatal(http.ListenAndServe(":8081", nil))
}

// А это функция-помощник, которую вызывает сервер, когда Java присылает сообщение
func (a *Agent) handleChat(w http.ResponseWriter, r *http.Request) {
	// Проверяем: нам прислали именно POST запрос (то есть нам принесли данные)?
	if r.Method != http.MethodPost {
		http.Error(w, "Java должна отправлять только POST запросы!", http.StatusMethodNotAllowed)
		return
	}

	// Достаем из посылки текст, который лежал под ярлыком "message"
	userMessage := r.FormValue("message")
	if userMessage == "" {
		http.Error(w, "Сообщение пустое!", http.StatusBadRequest)
		return
	}

	// Отправляем этот текст Клоду в Антропик
	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     "claude-haiku-4-5-20251001",              // Какую модель используем
		MaxTokens: 2048,                                     // Максимальная длина ответа
		System:    "Ты ИИ Джарвис. Отвечай коротко и емко.", // Твой системный промпт
		Messages: []anthropic.Message{
			{
				Role: anthropic.RoleUser,
				Content: []anthropic.MessageContent{
					anthropic.NewTextMessageContent(
						fmt.Sprintf("Инструкция: Ты — Джарвис. Используй HTML (<b>, <i>, <code>). Запрещены *, ** и #.\n\nЗапрос пользователя: %s", userMessage),
					),
				},
			},
		},
	})

	// Если по дороге к Клоду что-то сломалось (например, нет интернета)
	if err != nil {
		log.Printf("Ошибка при вызове Claude API: %v", err)
		http.Error(w, "Клод вредничает: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Если всё хорошо — берем текст ответа Клода и отдаем его обратно Java
	fmt.Fprint(w, *resp.Content[0].Text)
}
