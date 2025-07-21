# Использование официального образа Go для сборки
FROM golang:1.22-alpine AS builder

# Установка зависимостей сборки (если нужно)
RUN apk add --no-cache git

# Установка рабочей директории
WORKDIR /app

# Копирование go.mod и go.sum и загрузка зависимостей
COPY go.mod ./
COPY go.sum ./
RUN go mod download

# Копирование исходного кода приложения
COPY . .

# Сборка Go-приложения
# -o main указывает имя выходного исполняемого файла
# -ldflags="-s -w" уменьшает размер бинарника, удаляя отладочную информацию
RUN CGO_ENABLED=0 GOOS=linux go build -o main -ldflags="-s -w" .

# Использование минимального базового образа для финального образа (меньший размер)
FROM alpine:latest

# Установка рабочей директории
WORKDIR /app

# Копирование исполняемого файла из образа сборщика
COPY --from=builder /app/main .
COPY --from=builder /app/frame.png . # Убедитесь, что frame.png копируется!

# Определение порта, который будет слушать приложение
ENV PORT 8080
EXPOSE 8080

# Запуск исполняемого файла
CMD ["/app/main"]