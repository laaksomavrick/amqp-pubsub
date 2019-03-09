package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/streadway/amqp"
)

var (
	uri          = flag.String("uri", "amqp://rabbitmq:rabbitmq@localhost:5672/", "AMQP URI")
	exchangeName = flag.String("exchange", "pingponger-exchange", "Durable AMQP exchange name")
	exchangeType = flag.String("exchange-type", "direct", "Exchange type - direct|fanout|topic|x-custom")
	routingKey   = flag.String("routingKey", "pingponger-key", "AMQP routing key")
	body         = flag.String("body", "ping pong", "Body of message")
	reliable     = flag.Bool("reliable", true, "Wait for the publisher confirmation before exiting")
	queueName    = flag.String("queue", "test-queue", "Ephemeral AMQP queue name")
	bindingKey   = flag.String("bindingKey", "pingponger-key", "AMQP binding key")
	consumerTag  = flag.String("consumer-tag", "simple-consumer", "AMQP consumer tag (should not be blank)")
	lifetime     = flag.Duration("lifetime", 5*time.Second, "lifetime of process before shutdown (0s=infinite)")
)

type Consumer struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	tag     string
	done    chan error
}

func (c *Consumer) shutdown() error {
	// will close() the deliveries channel
	if err := c.channel.Cancel(c.tag, true); err != nil {
		return fmt.Errorf("Consumer cancel failed: %s", err)
	}

	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("AMQP connection close error: %s", err)
	}

	defer log.Printf("AMQP shutdown OK")

	// wait for handle() to exit
	return <-c.done
}

func init() {
	flag.Parse()
}

func main() {

	// get connection
	connection, err := newConnection(*uri)
	if err != nil {
		log.Fatalf("%s", err)
	}
	defer connection.Close()

	go work(connection, *exchangeName, *exchangeType, *routingKey, *body)

	c, err := newConsumer(*uri, *exchangeName, *exchangeType, *queueName, *bindingKey, *consumerTag)
	if err != nil {
		log.Fatalf("%s", err)
	}

	if *lifetime > 0 {
		log.Printf("running for %s", *lifetime)
		time.Sleep(*lifetime)
	} else {
		log.Printf("running forever")
		select {}
	}

	log.Printf("shutting down")

	if err := c.shutdown(); err != nil {
		log.Fatalf("error during shutdown: %s", err)
	}

}

func work(connection *amqp.Connection, exchangeName string, exchangeType string, routingKey string, body string) {
	channel, err := newChannel(connection, exchangeName, exchangeType)
	if err != nil {
		log.Fatalf("%s", err)
	}
	defer channel.Close()

	for {
		_ = publish(channel, exchangeName, routingKey, body)
		time.Sleep(1 * time.Second)
	}
}

func handle(deliveries <-chan amqp.Delivery, done chan error) {
	for d := range deliveries {
		log.Printf(
			"got %dB delivery: [%v] %s",
			len(d.Body),
			d.DeliveryTag,
			d.Body,
		)
	}
	log.Printf("handle: deliveries channel closed")
	done <- nil
}

func publish(channel *amqp.Channel, exchangeName string, routingKey string, body string) error {
	log.Printf("declared Exchange, publishing %dB body (%q)", len(body), body)
	if err := channel.Publish(
		exchangeName, // publish to an exchange
		routingKey,   // routing to 0 or more queues
		false,        // mandatory
		false,        // immediate
		amqp.Publishing{
			Headers:         amqp.Table{},
			ContentType:     "text/plain",
			ContentEncoding: "",
			Body:            []byte(body),
			DeliveryMode:    amqp.Transient, // 1=non-persistent, 2=persistent
			Priority:        0,              // 0-9
			// a bunch of application/implementation-specific fields
		},
	); err != nil {
		return fmt.Errorf("Exchange Publish: %s", err)
	}
	return nil
}

func newConsumer(amqpURI, exchange, exchangeType, queue, key, ctag string) (*Consumer, error) {
	c := &Consumer{
		conn:    nil,
		channel: nil,
		tag:     ctag,
		done:    make(chan error),
	}

	var err error

	log.Printf("dialing %s", amqpURI)
	c.conn, err = amqp.Dial(amqpURI)
	if err != nil {
		return nil, fmt.Errorf("Dial: %s", err)
	}

	log.Printf("got Connection, getting Channel")
	c.channel, err = c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("Channel: %s", err)
	}

	log.Printf("got Channel, declaring Exchange (%s)", exchange)
	if err = c.channel.ExchangeDeclare(
		exchange,     // name of the exchange
		exchangeType, // type
		true,         // durable
		false,        // delete when complete
		false,        // internal
		false,        // noWait
		nil,          // arguments
	); err != nil {
		return nil, fmt.Errorf("Exchange Declare: %s", err)
	}

	log.Printf("declared Exchange, declaring Queue (%s)", queue)
	state, err := c.channel.QueueDeclare(
		queue, // name of the queue
		true,  // durable
		false, // delete when usused
		false, // exclusive
		false, // noWait
		nil,   // arguments
	)
	if err != nil {
		return nil, fmt.Errorf("Queue Declare: %s", err)
	}

	log.Printf("declared Queue (%d messages, %d consumers), binding to Exchange (key '%s')",
		state.Messages, state.Consumers, key)

	if err = c.channel.QueueBind(
		queue,    // name of the queue
		key,      // bindingKey
		exchange, // sourceExchange
		false,    // noWait
		nil,      // arguments
	); err != nil {
		return nil, fmt.Errorf("Queue Bind: %s", err)
	}

	log.Printf("Queue bound to Exchange, starting Consume (consumer tag '%s')", c.tag)
	deliveries, err := c.channel.Consume(
		queue, // name
		c.tag, // consumerTag,
		false, // noAck
		false, // exclusive
		false, // noLocal
		false, // noWait
		nil,   // arguments
	)
	if err != nil {
		return nil, fmt.Errorf("Queue Consume: %s", err)
	}

	go handle(deliveries, c.done)

	return c, nil
}

// channels that can be thought of as "lightweight connections that share a single TCP connection".
// ...applications that use multiple threads/processes, open a new channel per thread/process
// https://www.rabbitmq.com/channels.html#basics
func newChannel(connection *amqp.Connection, exchangeName string, exchangeType string) (*amqp.Channel, error) {
	log.Printf("got Connection, getting Channel")
	channel, err := connection.Channel()
	if err != nil {
		return nil, fmt.Errorf("Channel: %s", err)
	}

	// create or verify exchange
	log.Printf("got Channel, declaring %q Exchange (%q)", exchangeType, exchangeName)
	if err := channel.ExchangeDeclare(
		exchangeName, // name
		exchangeType, // type
		true,         // durable
		false,        // auto-deleted
		false,        // internal
		false,        // noWait
		nil,          // arguments
	); err != nil {
		return nil, fmt.Errorf("Exchange Declare: %s", err)
	}

	// todo: no reliability / acking; irl would need this

	return channel, nil
}

func newConnection(amqpURI string) (*amqp.Connection, error) {
	log.Printf("dialing %q", amqpURI)
	connection, err := amqp.Dial(amqpURI)
	if err != nil {
		return nil, fmt.Errorf("Dial: %s", err)
	}
	return connection, nil
}
