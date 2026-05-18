package com.michael.jarvis_bot;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.context.annotation.Bean;
import org.telegram.telegrambots.meta.TelegramBotsApi;
import org.telegram.telegrambots.updatesreceivers.DefaultBotSession;

@SpringBootApplication
public class JarvisBotApplication {

    public static void main(String[] args) {
        SpringApplication.run(JarvisBotApplication.class, args);
        System.out.println("---------------------------------------");
        System.out.println("СИСТЕМА ЗАПУЩЕНА! ЖДУ СООБЩЕНИЙ...");
        System.out.println("---------------------------------------");
    }

    // Этот блок ПРИНУДИТЕЛЬНО включает твоего бота
    @Bean
    public TelegramBotsApi telegramBotsApi(JarvisBot jarvisBot) throws Exception {
        TelegramBotsApi botsApi = new TelegramBotsApi(DefaultBotSession.class);
        botsApi.registerBot(jarvisBot);
        System.out.println("БОТ УСПЕШНО ЗАРЕГИСТРИРОВАН В TELEGRAM! ✅");
        return botsApi;
    }
}