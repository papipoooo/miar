package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const exchangeName = "ikbo-64-23_zhurov_fanout"

func main() {
	queueName := flag.String(
		"queue",
		"ikbo-64-23_zhurov",
		"Название очереди",
	)
	flag.Parse()

	conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		log.Fatal(err)
	}
	defer ch.Close()

	err = ch.ExchangeDeclare(
		exchangeName, "fanout",
		false, false, false, false, nil,
	)
	if err != nil {
		log.Fatal(err)
	}

	queue, err := ch.QueueDeclare(
		*queueName,
		false, // durable
		true,  // auto-delete
		false, // exclusive
		false, // no-wait
		nil,
	)
	if err != nil {
		log.Fatal(err)
	}

	err = ch.QueueBind(
		queue.Name, "", exchangeName, false, nil,
	)
	if err != nil {
		log.Fatal(err)
	}

	messages, err := ch.Consume(
		queue.Name, "", false, false, false, false, nil,
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Ожидание сообщений. Очередь:", queue.Name)

	for msg := range messages {
		delay := rand.Intn(5) + 1

		fmt.Printf("Получено: %s\n", msg.Body)
		fmt.Printf("Обработка: %d сек.\n", delay)

		time.Sleep(time.Duration(delay) * time.Second)

		fmt.Println("Обработка завершена")
		if err := msg.Ack(false); err != nil {
			log.Println("Ошибка подтверждения:", err)
		}
	}
}
