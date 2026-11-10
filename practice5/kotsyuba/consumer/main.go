package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	exchangeName = "ikbo-64-23_kotsyuba_topic"
	queueName    = "ikbo-64-23_kotsyuba"
	bindingKey   = "marketplace.*.*"
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

	q, err := ch.QueueDeclare(queueName, false, true, false, false, nil)
	if err != nil {
		log.Fatal("QueueDeclare: ", err)
	}

	if err := ch.QueueBind(q.Name, bindingKey, exchangeName, false, nil); err != nil {
		log.Fatal("QueueBind: ", err)
	}

	messages, err := ch.Consume(q.Name, "", false, false, false, false, nil)
	if err != nil {
		log.Fatal("Consume: ", err)
	}

	fmt.Printf("Очередь: %s\n", q.Name)
	fmt.Println("Ожидание сообщений...")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-messages:
			if !ok {
				return
			}
			body := string(msg.Body)
			delay := strings.Count(body, ".")
			fmt.Printf("Получено: %s [%s]\n", body, msg.RoutingKey)
			fmt.Printf("Задержка: %d с\n", delay)
			time.Sleep(time.Duration(delay) * time.Second)
			if err := msg.Ack(false); err != nil {
				log.Fatal("Ack: ", err)
			}
			fmt.Println("Подтверждено")
		}
	}
}
