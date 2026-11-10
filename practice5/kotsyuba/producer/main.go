package main

import (
	"context"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	exchangeName = "ikbo-64-23_kotsyuba_topic"
	amqpURL      = "amqp://guest:guest@127.0.0.1:5672/"
)

func main() {
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		log.Fatalf("Подключение к RabbitMQ: %v", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		log.Fatal(err)
	}
	defer ch.Close()

	if err := ch.ExchangeDeclare(exchangeName, "topic", false, false, false, false, nil); err != nil {
		log.Fatal("ExchangeDeclare: ", err)
	}

	examples := []struct{ routingKey, body string }{
		{"marketplace.orders.created", "Заказ создан."},
		{"marketplace.payments.confirmed", "Оплата подтверждена.."},
		{"marketplace.orders.completed", "Заказ завершён..."},
		{"system.audit.logged", "Системное сообщение...."},
	}

	for _, m := range examples {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = ch.PublishWithContext(ctx, exchangeName, m.routingKey, false, false, amqp.Publishing{
			ContentType: "text/plain; charset=utf-8",
			Body:        []byte(m.body),
		})
		cancel()
		if err != nil {
			log.Fatalf("Отправка: %v", err)
		}
		fmt.Printf("Отправлено: %s [%s]\n", m.body, m.routingKey)
	}
}
