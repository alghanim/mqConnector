package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/IBM/sarama"
)

// kafkaOffsets is the operator-facing utility for the Kafka offset
// reset hazard documented in UPGRADING.md. The pre-1.1 binary used
// ConsumePartition WITHOUT a consumer group, so the broker never
// stored an offset; post-1.1 uses a real consumer group and the
// upgrade window can either replay (initial=oldest) or skip
// (initial=newest) — depending on what the operator wants.
//
// This command lets an operator deterministically place the consumer
// group's committed offset at the topic's current end (or beginning)
// BEFORE starting the new binary, so the choice is unambiguous and
// doesn't depend on which message arrives first.
//
// Invocation examples:
//
//	# Reset to end-of-topic (no replay):
//	mqconnector kafka-offsets --brokers=broker:9092 \
//	  --topic=orders --group-id=mqconnector-XXXX --to=latest
//
//	# Reset to start-of-retention (full replay):
//	mqconnector kafka-offsets --brokers=broker:9092 \
//	  --topic=orders --group-id=mqconnector-XXXX --to=earliest
//
//	# Print the auto-derived group id so an operator can find it
//	# without spelunking the source connector:
//	mqconnector kafka-offsets --brokers=broker:9092 \
//	  --topic=orders --print-group-id
//
// Intentionally a separate binary subcommand and not a config-file
// trigger — operators want this to be a one-shot they can run from a
// jumpbox before the upgrade, with the result printed back so they
// can verify before unblocking producers.
func kafkaOffsets() error {
	fs := flag.NewFlagSet("kafka-offsets", flag.ContinueOnError)
	brokers := fs.String("brokers", "", "comma-separated broker list (e.g. broker1:9092,broker2:9092)")
	topic := fs.String("topic", "", "Kafka topic name")
	groupID := fs.String("group-id", "", "consumer group id; omit + use --print-group-id to derive it")
	to := fs.String("to", "", "offset to set: 'latest' (current end) or 'earliest' (oldest retained)")
	printGroup := fs.Bool("print-group-id", false, "print the auto-derived group id for this brokers+topic and exit")
	dryRun := fs.Bool("dry-run", false, "print what would be committed but don't write")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	if *brokers == "" || *topic == "" {
		return errors.New("--brokers and --topic are required")
	}
	brokerList := strings.Split(*brokers, ",")

	// Auto-derive id with the same SHA-256-of-brokers+topic scheme
	// the Kafka connector uses (see groupIDFor in connector_kafka.go).
	// Operators reading this output can plug it back into the
	// connection row's group_id field or feed it to
	// `kafka-consumer-groups.sh` directly.
	derivedID := deriveKafkaGroupID(brokerList, *topic)
	if *printGroup {
		fmt.Println(derivedID)
		return nil
	}
	if *groupID == "" {
		*groupID = derivedID
	}

	var seekTo int64
	switch strings.ToLower(*to) {
	case "latest", "newest", "end":
		seekTo = sarama.OffsetNewest
	case "earliest", "oldest", "beginning", "start":
		seekTo = sarama.OffsetOldest
	default:
		return fmt.Errorf("--to must be 'latest' or 'earliest', got %q", *to)
	}

	scfg := sarama.NewConfig()
	scfg.Version = sarama.V2_5_0_0
	client, err := sarama.NewClient(brokerList, scfg)
	if err != nil {
		return fmt.Errorf("kafka client: %w", err)
	}
	defer client.Close()

	if err := client.RefreshMetadata(*topic); err != nil {
		return fmt.Errorf("kafka refresh metadata for %q: %w", *topic, err)
	}
	parts, err := client.Partitions(*topic)
	if err != nil {
		return fmt.Errorf("kafka partitions for %q: %w", *topic, err)
	}
	if len(parts) == 0 {
		return fmt.Errorf("topic %q has no partitions visible to this client", *topic)
	}

	mgr, err := sarama.NewOffsetManagerFromClient(*groupID, client)
	if err != nil {
		return fmt.Errorf("offset manager: %w", err)
	}
	defer mgr.Close()

	for _, p := range parts {
		target, err := client.GetOffset(*topic, p, seekTo)
		if err != nil {
			return fmt.Errorf("get %s offset partition %d: %w", *to, p, err)
		}
		fmt.Fprintf(os.Stderr, "partition %d → offset %d (%s)\n", p, target, *to)
		if *dryRun {
			continue
		}
		pm, err := mgr.ManagePartition(*topic, p)
		if err != nil {
			return fmt.Errorf("manage partition %d: %w", p, err)
		}
		// MarkOffset records the offset against the consumer group.
		// Commit then flushes to the broker before we close — without
		// it, the offset only lives in the in-memory manager.
		pm.MarkOffset(target, "")
		mgr.Commit()
		if err := pm.Close(); err != nil {
			return fmt.Errorf("close partition manager %d: %w", p, err)
		}
	}
	if *dryRun {
		fmt.Fprintln(os.Stderr, "dry-run: no offsets written")
	} else {
		fmt.Fprintf(os.Stderr, "committed offsets for group %q on %d partition(s)\n",
			*groupID, len(parts))
	}
	return nil
}

// deriveKafkaGroupID mirrors the connector's auto-derivation so
// operators don't have to read internal/mq/connector_kafka.go to
// figure out the group id. Kept in lockstep with groupIDFor; the
// pre-1.1 -> 1.1 upgrade note in UPGRADING.md shows the bash form,
// this is the Go form.
func deriveKafkaGroupID(brokers []string, topic string) string {
	h := sha256.New()
	for _, b := range brokers {
		h.Write([]byte(b))
		h.Write([]byte{0})
	}
	h.Write([]byte(topic))
	return "mqconnector-" + hex.EncodeToString(h.Sum(nil))[:16]
}
