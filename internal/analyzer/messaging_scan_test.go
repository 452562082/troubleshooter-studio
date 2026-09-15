package analyzer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanMessagingEndpointsFindsLiteralProducerAndConsumerLocations(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/Orders.java": `kafkaTemplate.send("orders.created", payload); @RabbitListener(queues = "billing.jobs") void consume() {}`,
		"src/events.ts":   `producer.send({ topic: "users.changed", messages }); channel.publish("events", "user.updated", body);`,
		"src/socket.py":   `socket.send("not-a-kafka-topic")`,
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := ScanMessagingEndpoints("", root, nil)
	if len(got) != 4 {
		t.Fatalf("messaging endpoints = %+v", got)
	}
	wants := map[string]bool{"kafka|orders.created|producer": false, "rabbitmq|billing.jobs|consumer": false, "kafka|users.changed|producer": false, "rabbitmq|events|producer": false}
	for _, endpoint := range got {
		key := endpoint.Broker + "|" + endpoint.Destination + "|" + endpoint.Direction
		if _, ok := wants[key]; ok {
			wants[key] = endpoint.Source != "" && endpoint.Line == 1 && endpoint.Strength == "scanned_literal"
		}
	}
	for key, found := range wants {
		if !found {
			t.Errorf("missing %s in %+v", key, got)
		}
	}
}

func TestScanMessagingEndpointsDistinguishesGoKafkaReaderFromMessageProducer(t *testing.T) {
	root := t.TempDir()
	content := `
reader := kafka.NewReader(kafka.ReaderConfig{Topic: "orders.created"})
writer.WriteMessages(ctx, kafka.Message{Topic: "orders.processed"})
ch.PublishWithContext(ctx, "events", "order.done", false, false, msg)
`
	if err := os.WriteFile(filepath.Join(root, "events.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ScanMessagingEndpoints("go", root, nil)
	wants := map[string]bool{"kafka|consumer|orders.created": false, "kafka|producer|orders.processed": false, "rabbitmq|producer|events|order.done": false}
	for _, endpoint := range got {
		key := endpoint.Broker + "|" + endpoint.Direction + "|" + endpoint.Destination
		if endpoint.RoutingKey != "" {
			key += "|" + endpoint.RoutingKey
		}
		if _, ok := wants[key]; ok {
			wants[key] = true
		}
	}
	for key, found := range wants {
		if !found {
			t.Errorf("missing %s in %+v", key, got)
		}
	}
}
