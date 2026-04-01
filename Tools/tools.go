package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"mqConnector/Data"
	"mqConnector/metrics"
	"mqConnector/models"
	"mqConnector/mq"
	"mqConnector/validation"
	"strings"
	"time"

	"github.com/beevik/etree"
)

// StartRoutine is the main message processing loop for a filter.
// It receives messages from a source MQ, applies the configured pipeline
// (validation, filtering, transforms, translation, routing), and forwards
// to one or more destination MQs.
func StartRoutine(ctx context.Context, config models.RoutineConfig) error {
	sourceConn := config.SourceConn
	destConn := config.DefaultDest
	filterRecord := config.FilterRecord

	sourceMQConnector, err := mq.NewMQConnector(mq.GetQueueType(sourceConn["type"]), sourceConn)
	if err != nil {
		return fmt.Errorf("failed to create source connector: %v", err)
	}

	defaultDestConnector, err := mq.NewMQConnector(mq.GetQueueType(destConn["type"]), destConn)
	if err != nil {
		return fmt.Errorf("failed to create destination connector: %v", err)
	}

	err = sourceMQConnector.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to source MQ: %v", err)
	}
	defer sourceMQConnector.Disconnect()

	err = defaultDestConnector.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to destination MQ: %v", err)
	}
	defer defaultDestConnector.Disconnect()

	Data.AddEntry(filterRecord.Id, sourceConn, destConn, config.FilterPaths)

	// Register with metrics
	metricsStore := metrics.GetStore()
	metricsStore.Register(filterRecord.Id, sourceConn["queueName"], destConn["queueName"])
	defer metricsStore.Unregister(filterRecord.Id)

	// Build pipeline if stages are configured
	pipeline := BuildPipeline(config.Stages, config.FilterPaths, config.Transforms, config.RoutingRules, config.OutputFormat)

	for {
		select {
		case <-ctx.Done():
			log.Printf("Stopping connection for filter: %s", filterRecord.Id)
			metricsStore.SetStatus(filterRecord.Id, "disconnected", "")
			return nil
		default:
			startTime := time.Now()

			msg, err := sourceMQConnector.ReceiveMessage()
			if err != nil {
				return fmt.Errorf("failed to receive message: %v", err)
			}

			format, err := DetectFormat(msg)
			if err != nil {
				if config.DLQFunc != nil {
					config.DLQFunc(msg, "unknown message format")
				}
				metricsStore.RecordFailure(filterRecord.Id)
				continue
			}

			// Schema validation (if configured)
			if config.SchemaType != "" && config.SchemaContent != "" {
				if err := validation.Validate(msg, format, config.SchemaType, config.SchemaContent); err != nil {
					if config.DLQFunc != nil {
						config.DLQFunc(msg, fmt.Sprintf("schema validation failed: %v", err))
					}
					metricsStore.RecordFailure(filterRecord.Id)
					continue
				}
			}

			var response []byte
			var finalFormat string

			if pipeline != nil {
				// Use pipeline processing
				result, newFormat, err := pipeline.Execute(msg, format)
				if err != nil {
					if config.DLQFunc != nil {
						config.DLQFunc(msg, fmt.Sprintf("pipeline error: %v", err))
					}
					metricsStore.RecordFailure(filterRecord.Id)
					continue
				}
				response = result
				finalFormat = newFormat

				// Send to routed destinations if any
				if len(pipeline.RoutingResults) > 0 {
					for _, rr := range pipeline.RoutingResults {
						if err := sendToDestination(rr.DestConn, response); err != nil {
							log.Printf("Failed to send to routed destination: %v", err)
							if config.DLQFunc != nil {
								config.DLQFunc(msg, fmt.Sprintf("routing send failed: %v", err))
							}
						}
					}
					pipeline.RoutingResults = nil // Reset for next message
				} else {
					// No routing results — send to default destination
					if response != nil {
						if err := defaultDestConnector.SendMessage(response); err != nil {
							if config.DLQFunc != nil {
								config.DLQFunc(msg, fmt.Sprintf("send failed: %v", err))
							}
							metricsStore.RecordFailure(filterRecord.Id)
							continue
						}
					}
				}
			} else {
				// Legacy processing (no pipeline stages configured)
				response, err = legacyProcess(msg, format, config)
				if err != nil {
					if config.DLQFunc != nil {
						config.DLQFunc(msg, fmt.Sprintf("processing error: %v", err))
					}
					metricsStore.RecordFailure(filterRecord.Id)
					continue
				}
				finalFormat = format

				// Evaluate routing rules
				if len(config.RoutingRules) > 0 {
					matched, err := EvaluateRoutingRules(msg, format, config.RoutingRules)
					if err == nil && len(matched) > 0 {
						for _, rule := range matched {
							if err := sendToDestination(rule.DestConn, response); err != nil {
								log.Printf("Failed to send to routed destination: %v", err)
							}
						}
					} else {
						// No routing match — send to default
						if response != nil {
							if err := defaultDestConnector.SendMessage(response); err != nil {
								if config.DLQFunc != nil {
									config.DLQFunc(msg, fmt.Sprintf("send failed: %v", err))
								}
								metricsStore.RecordFailure(filterRecord.Id)
								continue
							}
						}
					}
				} else {
					// No routing rules — send to default destination
					if response != nil {
						if err := defaultDestConnector.SendMessage(response); err != nil {
							if config.DLQFunc != nil {
								config.DLQFunc(msg, fmt.Sprintf("send failed: %v", err))
							}
							metricsStore.RecordFailure(filterRecord.Id)
							continue
						}
					}
				}
			}

			_ = finalFormat // available for future use (e.g., logging)
			elapsed := time.Since(startTime)
			if response != nil {
				metricsStore.RecordSuccess(filterRecord.Id, int64(len(response)), float64(elapsed.Milliseconds()))
			}
		}
	}
}

// legacyProcess applies the original filter -> transform -> translate pipeline.
func legacyProcess(msg []byte, format string, config models.RoutineConfig) ([]byte, error) {
	var response []byte

	// Step 1: Filter
	if format == "XML" {
		doc := etree.NewDocument()
		if err := doc.ReadFromString(string(msg)); err != nil {
			return nil, fmt.Errorf("XML parse error: %v", err)
		}
		namespace := doc.Root().Space
		for _, p := range config.FilterPaths {
			RemoveElements(doc.Root(), namespace, p)
		}
		result, err := doc.WriteToString()
		if err != nil {
			return nil, fmt.Errorf("XML write error: %v", err)
		}
		response = []byte(result)
	} else if format == "JSON" {
		filteredJson, err := RemoveJSONPaths(msg, config.FilterPaths)
		if err != nil {
			return nil, fmt.Errorf("JSON filter error: %v", err)
		}
		response = filteredJson
	} else {
		response = msg
	}

	// Step 2: Transform
	if len(config.Transforms) > 0 && response != nil {
		transformed, err := ApplyTransforms(response, format, config.Transforms)
		if err != nil {
			return nil, fmt.Errorf("transform error: %v", err)
		}
		response = transformed
	}

	// Step 3: Translate format
	if config.OutputFormat != "" && config.OutputFormat != "same" && response != nil {
		translated, err := TranslateFormat(response, format, config.OutputFormat)
		if err != nil {
			return nil, fmt.Errorf("translation error: %v", err)
		}
		response = translated
	}

	return response, nil
}

// sendToDestination creates a pooled connection and sends a message.
func sendToDestination(destConn map[string]string, message []byte) error {
	queueType := mq.GetQueueType(destConn["type"])
	connID := fmt.Sprintf("%s-%s-%s", destConn["type"], destConn["connName"], destConn["queueName"])

	connector, err := mq.Pool.Get(connID, queueType, destConn)
	if err != nil {
		return fmt.Errorf("failed to get connector: %v", err)
	}

	return connector.SendMessage(message)
}

// ExtractTags recursively extracts all tag names from an XML element.
func ExtractTags(element *etree.Element, tagSet map[string]struct{}) {
	if element == nil {
		return
	}
	tagSet[element.Tag] = struct{}{}
	for _, child := range element.ChildElements() {
		ExtractTags(child, tagSet)
	}
}

func nodeToMap(nodes []models.Node, path string, config map[string]bool) {
	for _, node := range nodes {
		currentPath := path
		if path != "" {
			currentPath += "."
		}
		currentPath += node.XMLName.Local
		config[currentPath] = true
		if len(node.Nodes) > 0 {
			nodeToMap(node.Nodes, currentPath, config)
		}
	}
}

func appendConfigPaths(config map[string]bool) (pathConfig []models.ConfigEntry) {
	for path, enabled := range config {
		pathConfig = append(pathConfig, models.ConfigEntry{
			FieldPath: path,
			Enabled:   enabled,
		})
	}
	return pathConfig
}

// ExtractPaths parses XML and extracts all field paths.
func ExtractPaths(fileBytes []byte) []models.ConfigEntry {
	var nodes models.Node
	if err := xml.Unmarshal(fileBytes, &nodes); err != nil {
		log.Printf("Failed to parse XML for path extraction: %v", err)
		return nil
	}

	config := make(map[string]bool)
	if len(nodes.Nodes) > 0 {
		nodeToMap(nodes.Nodes, "", config)
	}

	return appendConfigPaths(config)
}

// GetRootTag returns the root element tag name from XML bytes.
func GetRootTag(xmlBytes []byte) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(xmlBytes)))

	for {
		t, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		switch se := t.(type) {
		case xml.StartElement:
			return se.Name.Local, nil
		}
	}

	return "", fmt.Errorf("no root element found")
}

// RemoveJSONPaths removes the specified dot-separated paths from a JSON document.
func RemoveJSONPaths(originalJSON []byte, pathsToRemove []string) ([]byte, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(originalJSON, &data); err != nil {
		return nil, err
	}

	for _, path := range pathsToRemove {
		keys := strings.Split(path, ".")
		deletePath(&data, keys)
	}

	return json.Marshal(data)
}

func deletePath(data *map[string]interface{}, keys []string) {
	current := *data
	for i, key := range keys {
		if i == len(keys)-1 {
			delete(current, key)
		} else {
			if next, ok := current[key].(map[string]interface{}); ok {
				current = next
			} else {
				return
			}
		}
	}
}

// DetectFormat returns whether a message is XML or JSON.
func DetectFormat(message []byte) (string, error) {
	if IsJSON(message) {
		return "JSON", nil
	} else if IsXML(message) {
		return "XML", nil
	}
	return "", fmt.Errorf("unknown format")
}

func IsJSON(message []byte) bool {
	var js json.RawMessage
	return json.Unmarshal(message, &js) == nil
}

func IsXML(message []byte) bool {
	return xml.Unmarshal(message, new(interface{})) == nil
}

// RemoveElements recursively removes XML elements matching namespace and tag.
func RemoveElements(elem *etree.Element, namespace, tag string) {
	for _, child := range elem.ChildElements() {
		if child.Space == namespace && child.Tag == tag {
			elem.RemoveChild(child)
		} else {
			RemoveElements(child, namespace, tag)
		}
	}
	for _, child := range elem.Child {
		if c, ok := child.(*etree.CharData); ok && strings.TrimSpace(c.Data) == "" {
			elem.RemoveChild(child)
		}
	}
}

// FindElement recursively finds the first element with the given namespace and tag.
func FindElement(elem *etree.Element, namespace, tag string) *etree.Element {
	if elem.Space == namespace && elem.Tag == tag {
		return elem
	}
	for _, child := range elem.ChildElements() {
		if found := FindElement(child, namespace, tag); found != nil {
			return found
		}
	}
	return nil
}
