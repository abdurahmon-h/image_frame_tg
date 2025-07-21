package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv" // !!! Добавлен импорт
	"github.com/nfnt/resize"
)

const (
	removeBgAPIURL  = "https://api.remove.bg/v1.0/removebg"
	framePath       = "frame.png"
	targetFrameSize = 1200
	padding         = 130
)

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

func main() {
	// --- Загрузка переменных окружения из .env файла (только для локальной разработки) ---
	// godotenv.Load() попытается загрузить .env файл.
	// Если переменные уже установлены в окружении (например, в Cloud Run),
	// то godotenv.Load() их не перезапишет.
	err := godotenv.Load()
	if err != nil {
		log.Println("Не удалось загрузить .env файл (это нормально для продакшена/облака):", err)
	}
	// --- Конец загрузки .env ---

	// --- Чтение токенов из переменных окружения ---
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatalf("Ошибка: Переменная окружения TELEGRAM_BOT_TOKEN не установлена. Установите ее в .env файле или как системную переменную.")
	}

	removeBgAPIKey := os.Getenv("REMOVE_BG_API_KEY")
	if removeBgAPIKey == "" {
		log.Fatalf("Ошибка: Переменная окружения REMOVE_BG_API_KEY не установлена. Установите ее в .env файле или как системную переменную.")
	}
	// --- Конец чтения токенов ---

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panicf("Ошибка инициализации Telegram бота: %v", err)
	}

	bot.Debug = true

	log.Printf("Авторизован как @%s", bot.Self.UserName)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	go func() {
		log.Printf("Запуск HTTP-сервера для Cloud Run Health Checks на порту %s", port)
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "Bot is running!")
		})
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Fatalf("КРИТИЧЕСКАЯ ОШИБКА: Не удалось запустить HTTP-сервер для Health Checks: %v", err)
		}
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	frameImage, err := loadFrame(framePath)
	if err != nil {
		log.Fatalf("КРИТИЧЕСКАЯ ОШИБКА: Не удалось загрузить рамку из '%s': %v. Убедитесь, что файл существует и является валидным изображением (PNG). Бот не может работать без рамки.", framePath, err)
	}

	frameImage = resize.Resize(targetFrameSize, targetFrameSize, frameImage, resize.Lanczos3)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		if update.Message.IsCommand() {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "")
			switch update.Message.Command() {
			case "start":
				msg.Text = "Привет! Отправь мне фото, и я добавлю к нему рамку 🎨, а затем попробую удалить фон!"
			default:
				msg.Text = "Неизвестная команда."
			}
			if _, err := bot.Send(msg); err != nil {
				log.Printf("Ошибка отправки сообщения '%s' пользователю %d: %v", msg.Text, update.Message.Chat.ID, err)
			}
		} else if update.Message.Photo != nil {
			handlePhoto(bot, update.Message, frameImage, removeBgAPIKey)
		} else {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Пожалуйста, отправьте мне фотографию.")
			if _, err := bot.Send(msg); err != nil {
				log.Printf("Ошибка отправки сообщения 'Пожалуйста, отправьте мне фотографию.' пользователю %d: %v", update.Message.Chat.ID, err)
			}
		}
	}
}

func sendErrorMessage(bot *tgbotapi.BotAPI, chatID int64, userMessage string, internalError error) {
	log.Printf("ВНУТРЕННЯЯ ОШИБКА для чата %d: %v. Сообщение пользователю: '%s'", chatID, internalError, userMessage)
	msg := tgbotapi.NewMessage(chatID, userMessage)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("КРИТИЧЕСКАЯ ОШИБКА: Не удалось отправить сообщение об ошибке пользователю %d: %v", chatID, err)
	}
}

func loadFrame(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть файл рамки '%s': %w", path, err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("не удалось декодировать рамку '%s': %w", path, err)
	}
	return img, nil
}

func resizeAndPlaceImage(userImage image.Image, frame image.Image, padding int) (image.Image, error) {
	frameBounds := frame.Bounds()
	frameWidth := frameBounds.Dx()
	frameHeight := frameBounds.Dy()

	maxContentWidth := frameWidth - 2*padding
	maxContentHeight := frameHeight - 2*padding

	if maxContentWidth <= 0 || maxContentHeight <= 0 {
		return nil, fmt.Errorf("недостаточный размер рамки (%dx%d) для заданных отступов (%d). Проверьте frame.png и значение padding.", frameWidth, frameHeight, padding)
	}

	userImageBounds := userImage.Bounds()
	userImageWidth := userImageBounds.Dx()
	userImageHeight := userImageBounds.Dy()

	scaleWidth := float64(maxContentWidth) / float64(userImageWidth)
	scaleHeight := float64(maxContentHeight) / float64(userImageHeight)

	scale := scaleWidth
	if scaleHeight < scale {
		scale = scaleHeight
	}

	newWidth := int(float64(userImageWidth) * scale)
	newHeight := int(float64(userImageHeight) * scale)

	resizedUserImage := resize.Resize(uint(newWidth), uint(newHeight), userImage, resize.Lanczos3)

	finalImage := image.NewRGBA(frameBounds)

	x := (frameWidth - resizedUserImage.Bounds().Dx()) / 2
	y := (frameHeight - resizedUserImage.Bounds().Dy()) / 2

	draw.Draw(finalImage, resizedUserImage.Bounds().Add(image.Point{x, y}), resizedUserImage, resizedUserImage.Bounds().Min, draw.Src)

	return finalImage, nil
}

func removeBackground(imageBytes []byte, apiKey string) ([]byte, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("image_file", "image.png")
	if err != nil {
		return nil, fmt.Errorf("ошибка создания формы файла для remove.bg: %w", err)
	}
	_, err = io.Copy(part, bytes.NewReader(imageBytes))
	if err != nil {
		return nil, fmt.Errorf("ошибка копирования байтов изображения в форму для remove.bg: %w", err)
	}

	err = writer.WriteField("size", "auto")
	if err != nil {
		return nil, fmt.Errorf("ошибка записи поля 'size' для remove.bg: %w", err)
	}

	err = writer.WriteField("format", "png")
	if err != nil {
		return nil, fmt.Errorf("ошибка записи поля 'format' для remove.bg: %w", err)
	}

	writer.Close()

	req, err := http.NewRequest("POST", removeBgAPIURL, body)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания HTTP-запроса к remove.bg: %w", err)
	}

	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "image/png")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка выполнения HTTP-запроса к remove.bg: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(resp.Body)
		var apiError struct {
			Errors []struct {
				Title string `json:"title"`
				Code  string `json:"code"`
			} `json:"errors"`
		}
		if json.Unmarshal(errorBody, &apiError) == nil && len(apiError.Errors) > 0 {
			return nil, fmt.Errorf("API remove.bg вернул ошибку (%d): %s (код: %s)", resp.StatusCode, apiError.Errors[0].Title, apiError.Errors[0].Code)
		}
		return nil, fmt.Errorf("API remove.bg вернул неуспешный статус: %s. Ответ: %s", resp.Status, string(errorBody))
	}

	resultBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа от remove.bg: %w", err)
	}

	return resultBytes, nil
}

func handlePhoto(bot *tgbotapi.BotAPI, message *tgbotapi.Message, frameImage image.Image, removeBgAPIKey string) {
	chatID := message.Chat.ID
	if len(message.Photo) == 0 {
		sendErrorMessage(bot, chatID, "Не удалось получить фотографию из вашего сообщения.", fmt.Errorf("получено сообщение без фото"))
		return
	}
	photo := message.Photo[len(message.Photo)-1]

	fileURL, err := bot.GetFileDirectURL(photo.FileID)
	if err != nil {
		sendErrorMessage(bot, chatID, "Не удалось получить ссылку на вашу фотографию. Пожалуйста, попробуйте еще раз.", fmt.Errorf("GetFileDirectURL для FileID %s: %w", photo.FileID, err))
		return
	}

	log.Printf("Прямой URL для скачивания: %s", fileURL)

	fileBytes, err := downloadFile(fileURL)
	if err != nil {
		sendErrorMessage(bot, chatID, "Не удалось скачать вашу фотографию. Пожалуйста, попробуйте еще раз.", fmt.Errorf("скачивание файла с URL '%s': %w", fileURL, err))
		return
	}

	userImage, imgFormat, err := image.Decode(bytes.NewReader(fileBytes))
	if err != nil {
		sendErrorMessage(bot, chatID, "Не удалось обработать формат вашей фотографии. Пожалуйста, отправьте изображение в формате JPG или PNG.", fmt.Errorf("декодирование изображения пользователя (формат: %s, размер байтов: %d): %w", imgFormat, len(fileBytes), err))
		return
	}
	log.Printf("Изображение пользователя успешно декодировано. Формат: %s, Размер: %dx%d", imgFormat, userImage.Bounds().Dx(), userImage.Bounds().Dy())

	imageOnCanvas, err := resizeAndPlaceImage(userImage, frameImage, padding)
	if err != nil {
		sendErrorMessage(bot, chatID, "Произошла ошибка при подготовке изображения к наложению рамки. Пожалуйста, попробуйте другую фотографию.", fmt.Errorf("масштабирование и размещение изображения: %w", err))
		return
	}

	finalCombinedImage := image.NewRGBA(frameImage.Bounds())
	draw.Draw(finalCombinedImage, finalCombinedImage.Bounds(), imageOnCanvas, imageOnCanvas.Bounds().Min, draw.Src)
	draw.Draw(finalCombinedImage, finalCombinedImage.Bounds(), frameImage, frameImage.Bounds().Min, draw.Over)

	framedImageBuf := new(bytes.Buffer)
	if err := png.Encode(framedImageBuf, finalCombinedImage); err != nil {
		sendErrorMessage(bot, chatID, "Не удалось подготовить изображение для удаления фона.", fmt.Errorf("кодирование обрамленного изображения в PNG для remove.bg: %w", err))
		return
	}

	log.Printf("Отправляю обрамленное изображение на remove.bg для удаления фона...")
	sendTempMessage(bot, chatID, "Ваше фото обрабатывается... Удаляю фон, это может занять немного времени.")

	imageWithNoBgBytes, err := removeBackground(framedImageBuf.Bytes(), removeBgAPIKey)
	if err != nil {
		sendErrorMessage(bot, chatID, "Не удалось удалить фон. Возможно, проблема с сервисом или API ключом, или лимитами. Попробуйте другую фотографию или проверьте ваш remove.bg аккаунт.", fmt.Errorf("удаление фона с помощью remove.bg: %w", err))
		return
	}
	log.Printf("Фон успешно удален.")

	documentMsg := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{
		Name:  "framed_photo_no_bg.png",
		Bytes: imageWithNoBgBytes,
	})
	documentMsg.Caption = "Ваша фотография с рамкой и удаленным фоном (без сжатия)."

	if _, err := bot.Send(documentMsg); err != nil {
		log.Printf("Ошибка при отправке итогового фото как документа пользователю %d: %v", chatID, err)
	} else {
		log.Printf("Обработанное фото отправлено как документ пользователю %d.", chatID)
	}
}

func downloadFile(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("ошибка при HTTP-запросе к %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("неуспешный статус HTTP: %s. Ответ: %s (URL: %s)", resp.Status, string(errorBody), url)
	}

	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка при чтении тела ответа из %s: %w", url, err)
	}

	return buf.Bytes(), nil
}

func sendTempMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Не удалось отправить временное сообщение пользователю %d: %v", chatID, err)
	}
}
