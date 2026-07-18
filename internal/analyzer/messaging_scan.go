package analyzer

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type messagingPattern struct {
	re               *regexp.Regexp
	broker           string
	direction        string
	destinationKind  string
	destinationIndex int
	routingIndex     int
}

var messagingPatterns = []messagingPattern{
	{regexp.MustCompile(`(?i)\bKafkaTemplate(?:<[^>]+>)?[^\n;]*\.send\s*\(\s*["']([^"']+)["']`), "kafka", "producer", "topic", 1, 0},
	{regexp.MustCompile(`(?i)@KafkaListener\s*\([^)]*?(?:topics?\s*=\s*)?(?:\{\s*)?["']([^"']+)["']`), "kafka", "consumer", "topic", 1, 0},
	{regexp.MustCompile(`(?i)\b(?:producer\.)?send\s*\(\s*\{[^}]*?topic\s*:\s*["']([^"']+)["']`), "kafka", "producer", "topic", 1, 0},
	{regexp.MustCompile(`(?i)\bconsumer\.subscribe\s*\(\s*\{[^}]*?topic\s*:\s*["']([^"']+)["']`), "kafka", "consumer", "topic", 1, 0},
	{regexp.MustCompile(`(?i)\b(?:kafka\.)?Message\s*\{[^}]*?Topic\s*:\s*["']([^"']+)["']`), "kafka", "producer", "topic", 1, 0},
	{regexp.MustCompile(`(?i)\b(?:ReaderConfig|ConsumerConfig)\s*\{[^}]*?Topic\s*:\s*["']([^"']+)["']`), "kafka", "consumer", "topic", 1, 0},
	{regexp.MustCompile(`(?i)\b(?:producer|kafka_producer)\.(?:send|send_and_wait)\s*\(\s*["']([^"']+)["']`), "kafka", "producer", "topic", 1, 0},
	{regexp.MustCompile(`(?i)\b(?:consumer|kafka_consumer)\.subscribe\s*\(\s*\[?\s*["']([^"']+)["']`), "kafka", "consumer", "topic", 1, 0},
	{regexp.MustCompile(`(?i)\bconvertAndSend\s*\(\s*["']([^"']+)["']\s*,\s*["']([^"']+)["']`), "rabbitmq", "producer", "exchange", 1, 2},
	{regexp.MustCompile(`(?i)@RabbitListener\s*\([^)]*?queues?\s*=\s*(?:\{\s*)?["']([^"']+)["']`), "rabbitmq", "consumer", "queue", 1, 0},
	{regexp.MustCompile(`(?i)\b(?:channel|ch)\.publish\s*\(\s*["']([^"']*)["']\s*,\s*["']([^"']+)["']`), "rabbitmq", "producer", "exchange", 1, 2},
	{regexp.MustCompile(`\b(?:channel|ch)\.Publish\s*\(\s*["']([^"']*)["']\s*,\s*["']([^"']+)["']`), "rabbitmq", "producer", "exchange", 1, 2},
	{regexp.MustCompile(`\b(?:channel|ch)\.PublishWithContext\s*\(\s*[^,]+,\s*["']([^"']*)["']\s*,\s*["']([^"']+)["']`), "rabbitmq", "producer", "exchange", 1, 2},
	{regexp.MustCompile(`(?i)\b(?:channel|ch)\.consume\s*\(\s*["']([^"']+)["']`), "rabbitmq", "consumer", "queue", 1, 0},
	{regexp.MustCompile(`(?i)\bbasic_publish\s*\([^)]*?exchange\s*=\s*["']([^"']*)["'][^)]*?routing_key\s*=\s*["']([^"']+)["']`), "rabbitmq", "producer", "exchange", 1, 2},
	{regexp.MustCompile(`(?i)\bbasic_consume\s*\([^)]*?queue\s*=\s*["']([^"']+)["']`), "rabbitmq", "consumer", "queue", 1, 0},
}

var safeMessagingLiteralRE = regexp.MustCompile(`^[A-Za-z0-9._:/-]*$`)

func ScanMessagingEndpoints(stack, repoPath string, includePaths []string) []MessagingEndpoint {
	return ScanMessagingEndpointsContext(context.Background(), stack, repoPath, includePaths)
}

func ScanMessagingEndpointsContext(ctx context.Context, stack, repoPath string, includePaths []string) []MessagingEndpoint {
	files, _ := walkFilesContext(ctx, repoPath, includePaths, func(rel string) bool {
		if pathHasIgnoredSegment(rel) {
			return false
		}
		ext := strings.ToLower(filepath.Ext(rel))
		return ext == ".go" || ext == ".java" || ext == ".kt" || ext == ".js" || ext == ".jsx" || ext == ".ts" || ext == ".tsx" || ext == ".py"
	})
	seen := map[string]bool{}
	var result []MessagingEndpoint
	for _, file := range files {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(repoPath, file)
		source := filepath.ToSlash(rel)
		text := string(data)
		for _, pattern := range messagingPatterns {
			for _, indexes := range pattern.re.FindAllStringSubmatchIndex(text, -1) {
				match := pattern.re.FindStringSubmatch(text[indexes[0]:indexes[1]])
				if len(match) <= pattern.destinationIndex {
					continue
				}
				destination := safeMessagingLiteral(match[pattern.destinationIndex])
				routingKey := ""
				if pattern.routingIndex > 0 && len(match) > pattern.routingIndex {
					routingKey = safeMessagingLiteral(match[pattern.routingIndex])
				}
				if destination == "" && routingKey == "" {
					continue
				}
				key := strings.Join([]string{pattern.broker, pattern.direction, pattern.destinationKind, destination, routingKey, source}, "|")
				if seen[key] {
					continue
				}
				seen[key] = true
				result = append(result, MessagingEndpoint{Broker: pattern.broker, Direction: pattern.direction, DestinationKind: pattern.destinationKind, Destination: destination, RoutingKey: routingKey, Source: source, Line: lineNumberAt(text, indexes[0]), Strength: "scanned_literal"})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		return strings.Join([]string{left.Broker, left.Destination, left.Direction, left.Source}, "|") < strings.Join([]string{right.Broker, right.Destination, right.Direction, right.Source}, "|")
	})
	return result
}

func safeMessagingLiteral(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 || !safeMessagingLiteralRE.MatchString(value) || strings.Contains(value, "${") || strings.Contains(value, "#{") {
		return ""
	}
	return value
}
