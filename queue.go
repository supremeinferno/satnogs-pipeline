package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	queueObservations = "observations"
	queueTelemetry    = "telemetry"
)

// deadName is the dead-letter queue of a queue: where messages that can never
// be handled wait for a human.
func deadName(queue string) string { return queue + ".dead" }

// declare creates a queue and its dead queue if they don't exist yet;
// declaring again with the same settings changes nothing, so every program
// can call it on start. Both are durable (they survive a broker restart). A
// message rejected without requeue on the main queue is routed to the dead
// queue instead of being retried forever or lost.
func declare(ch *amqp.Channel, queue string) error {
	if _, err := ch.QueueDeclare(deadName(queue), true, false, false, false, nil); err != nil {
		return err
	}
	_, err := ch.QueueDeclare(queue, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    "",              // the default exchange...
		"x-dead-letter-routing-key": deadName(queue), // ...routes by queue name
	})
	return err
}

// publisher sends messages to queues. It connects on first use and again
// after any failure: amqp091 does not reconnect by itself.
type publisher struct {
	url  string
	conn *amqp.Connection
	ch   *amqp.Channel
}

func (p *publisher) connect() error {
	conn, err := amqp.Dial(p.url)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err == nil {
		for _, q := range []string{queueObservations, queueTelemetry} {
			if err = declare(ch, q); err != nil {
				break
			}
		}
	}
	if err == nil {
		// Publisher confirms: the broker acknowledges each message once it
		// has accepted it, so "published" really means "the queue has it".
		err = ch.Confirm(false)
	}
	if err != nil {
		conn.Close()
		return err
	}
	p.conn, p.ch = conn, ch
	return nil
}

func (p *publisher) close() {
	if p.conn != nil {
		p.conn.Close()
	}
	p.conn, p.ch = nil, nil
}

// publish sends v, as JSON, to queue and waits for the broker's confirmation.
func (p *publisher) publish(ctx context.Context, queue string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if p.ch == nil {
		if err := p.connect(); err != nil {
			return fmt.Errorf("connect to rabbitmq: %w", err)
		}
	}
	conf, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, "", queue, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent, // written to disk, survives a broker restart
		Body:         body,
	})
	if err != nil {
		p.close() // connection is broken; reconnect on the next call
		return fmt.Errorf("publish to %s: %w", queue, err)
	}
	acked, err := conf.WaitContext(ctx)
	if err != nil {
		p.close()
		return fmt.Errorf("confirm on %s: %w", queue, err)
	}
	if !acked {
		return fmt.Errorf("broker refused a message for %s", queue)
	}
	published.WithLabelValues(queue).Inc()
	return nil
}

// permanentError marks a failure that retrying cannot fix: the message goes
// to the dead queue.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return permanentError{err} }

// consume hands each message on queue to handle, until ctx is cancelled
// (returns nil) or the connection fails (returns the error; the caller
// reconnects).
//
// A message is acknowledged only after handle succeeds. If this program dies
// first, the broker gives the message to someone else: nothing is lost.
//   - handle returns nil: acknowledged.
//   - handle returns a permanent error (undecodable, invalid): rejected
//     without requeue, so it goes to the dead queue. Retrying cannot fix it,
//     and it must not block the rest.
//   - any other error (database down): requeued, after a pause, to try again.
func consume(ctx context.Context, url, queue string, handle func(context.Context, []byte) error) error {
	conn, err := amqp.Dial(url)
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	if err := declare(ch, queue); err != nil {
		return err
	}
	// At most 10 unacknowledged messages at a time, so a slow consumer isn't
	// buried and several consumers can share the queue fairly.
	if err := ch.Qos(10, 0, false); err != nil {
		return err
	}
	msgs, err := ch.Consume(queue, "", false, false, false, false, nil) // autoAck off
	if err != nil {
		return err
	}
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))

	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-closed:
			return fmt.Errorf("connection closed: %v", e)
		case m, ok := <-msgs:
			if !ok {
				return errors.New("delivery channel closed")
			}
			err := handle(ctx, m.Body)
			var perm permanentError
			switch {
			case err == nil:
				if err := m.Ack(false); err != nil {
					return err
				}
			case errors.As(err, &perm):
				log.Printf("%s: rejecting message to the dead queue: %v: %.120s", queue, err, m.Body)
				if err := m.Nack(false, false); err != nil {
					return err
				}
			default:
				log.Printf("%s: message failed, requeueing: %v", queue, err)
				if err := m.Nack(false, true); err != nil {
					return err
				}
				if sleep(ctx, 2*time.Second) != nil {
					return nil
				}
			}
		}
	}
}
