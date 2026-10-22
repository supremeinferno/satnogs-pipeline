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
	queueName = "observations"
	deadQueue = "observations.dead" // messages the consumer rejected for good
)

// declare creates both queues if they don't exist yet; declaring again with
// the same settings changes nothing, so every program can call it on start.
// Both are durable (they survive a broker restart). A message rejected
// without requeue on the main queue is routed to the dead queue, where it
// waits for a human instead of being retried forever or lost.
func declare(ch *amqp.Channel) error {
	if _, err := ch.QueueDeclare(deadQueue, true, false, false, false, nil); err != nil {
		return err
	}
	_, err := ch.QueueDeclare(queueName, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    "",        // the default exchange...
		"x-dead-letter-routing-key": deadQueue, // ...routes by queue name
	})
	return err
}

// publisher sends observations to the queue. It connects on first use and
// again after any failure: amqp091 does not reconnect by itself.
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
		err = declare(ch)
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

// publish sends one observation and waits for the broker's confirmation.
func (p *publisher) publish(ctx context.Context, o Observation) error {
	body, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if p.ch == nil {
		if err := p.connect(); err != nil {
			return fmt.Errorf("connect to rabbitmq: %w", err)
		}
	}
	conf, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, "", queueName, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent, // written to disk, survives a broker restart
		Body:         body,
	})
	if err != nil {
		p.close() // connection is broken; reconnect on the next call
		return fmt.Errorf("publish observation %d: %w", o.ID, err)
	}
	acked, err := conf.WaitContext(ctx)
	if err != nil {
		p.close()
		return fmt.Errorf("confirm observation %d: %w", o.ID, err)
	}
	if !acked {
		return fmt.Errorf("broker refused observation %d", o.ID)
	}
	published.Inc()
	return nil
}

// validate rejects messages that decode but make no sense.
func validate(o Observation) error {
	if o.ID <= 0 || o.NoradID <= 0 || o.Status == "" || o.End.Before(o.Start) {
		return errors.New("incomplete or inconsistent observation")
	}
	return nil
}

// consume hands each queued observation to handle, until ctx is cancelled
// (returns nil) or the connection fails (returns the error; the caller
// reconnects).
//
// A message is acknowledged only after handle succeeds. If this program dies
// first, the broker gives the message to someone else: nothing is lost.
//   - undecodable or invalid message: rejected without requeue, so it goes to
//     the dead queue. Retrying cannot fix it, and it must not block the rest.
//   - handle fails (database down): requeued, after a pause, to try again.
func consume(ctx context.Context, url string, handle func(context.Context, Observation) error) error {
	conn, err := amqp.Dial(url)
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	if err := declare(ch); err != nil {
		return err
	}
	// At most 10 unacknowledged messages at a time, so a slow consumer isn't
	// buried and several consumers can share the queue fairly.
	if err := ch.Qos(10, 0, false); err != nil {
		return err
	}
	msgs, err := ch.Consume(queueName, "", false, false, false, false, nil) // autoAck off
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
			var o Observation
			err := json.Unmarshal(m.Body, &o)
			if err == nil {
				err = validate(o)
			}
			if err != nil {
				log.Printf("rejecting message to dead queue: %v: %.120s", err, m.Body)
				stored.WithLabelValues("rejected").Inc()
				if err := m.Nack(false, false); err != nil {
					return err
				}
				continue
			}
			if err := handle(ctx, o); err != nil {
				log.Printf("observation %d failed, requeueing: %v", o.ID, err)
				if err := m.Nack(false, true); err != nil {
					return err
				}
				if sleep(ctx, 2*time.Second) != nil {
					return nil
				}
				continue
			}
			if err := m.Ack(false); err != nil {
				return err
			}
		}
	}
}
