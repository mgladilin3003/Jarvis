package com.michael.jarvis_bot;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;
import org.springframework.util.LinkedMultiValueMap;
import org.springframework.util.MultiValueMap;
import org.springframework.web.client.RestTemplate;
import org.telegram.telegrambots.bots.TelegramLongPollingBot;
import org.telegram.telegrambots.meta.api.methods.send.SendMessage;
import org.telegram.telegrambots.meta.api.objects.Update;
import org.telegram.telegrambots.meta.exceptions.TelegramApiException;

@Component // Это говорит Java: "Следи за этим классом, это наш бот!"
public class JarvisBot extends TelegramLongPollingBot {

    private final String botUsername;
    private final RestTemplate restTemplate; // Это наш "телефон" для звонков в Go

    public JarvisBot(@Value("${bot.name}") String botUsername, 
                     @Value("${bot.token}") String botToken) {
        super(botToken);
        this.botUsername = botUsername;
        this.restTemplate = new RestTemplate();
    }

    @Override
    public String getBotUsername() {
        return this.botUsername;
    }

    @Override
    public void onUpdateReceived(Update update) {
        if (update.hasMessage() && update.getMessage().hasText()) {
        // 1. Сначала достаем данные
            String messageFromUser = update.getMessage().getText();
            long chatId = update.getMessage().getChatId();

            // 2. А теперь печатаем их в консоль для проверки
            System.out.println("Получено сообщение из Telegram: " + messageFromUser);

            // 2. Звоним нашему "Повару" на порт 8081
            String goAgentUrl = "http://localhost:8081/api/v1/chat";
            
            // Собираем данные в посылку
            MultiValueMap<String, String> body = new LinkedMultiValueMap<>();
            body.add("message", messageFromUser);

            try {
                // Ждем, пока Повар (Go) приготовит ответ от Клода
                String responseFromClaude = restTemplate.postForObject(goAgentUrl, body, String.class);

                // 3. Отправляем ответ обратно в Telegram
                sendText(chatId, responseFromClaude);
            } catch (Exception e) {
                sendText(chatId, "Ошибка: Повар на кухне (Go) не отвечает!");
            }
        }
    }

private void sendText(long chatId, String text) {
        SendMessage sm = SendMessage.builder()
                .chatId(chatId)
                .text(text)
                .parseMode("HTML")
                .build();
        try {
            execute(sm);
        } catch (TelegramApiException e) {
            // Если Клод прислал символы, которые сломали MarkdownV2,
            // отправляем просто чистый текст, чтобы бот не молчал
            try {
                execute(SendMessage.builder()
                        .chatId(chatId)
                        .text(text)
                        .build());
            } catch (TelegramApiException ex) {
                ex.printStackTrace();
            }
        }
    }
}