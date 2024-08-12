package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"mqConnector/Data"
	"mqConnector/models"
	"mqConnector/mq"
	"strings"

	"github.com/beevik/etree"
	pbmodels "github.com/pocketbase/pocketbase/models"
)

func StartRoutine(ctx context.Context, filterPaths []string, sourceConn, destConn map[string]string, filterRecord *pbmodels.Record) error {
	var sourceMQConnector mq.MQConnector
	var destinationMQConnector mq.MQConnector

	sourceMQConnector, err := mq.NewMQConnector(mq.GetQueueType(sourceConn["type"]), sourceConn)
	if err != nil {
		return fmt.Errorf("Failed to create MQ source connector: %v", err)
	}

	destinationMQConnector, err = mq.NewMQConnector(mq.GetQueueType(destConn["type"]), destConn)
	if err != nil {
		return fmt.Errorf("Failed to create MQ destination connector: %v", err)
	}

	err = sourceMQConnector.Connect()
	if err != nil {
		return fmt.Errorf("Failed to connect to MQ: %v", err)
	}
	defer sourceMQConnector.Disconnect()

	Data.AddEntry(filterRecord.Id, sourceConn, destConn, filterPaths)
	jD, _ := json.Marshal(Data.DataMap)
	fmt.Println(string(jD))

	for {
		select {
		case <-ctx.Done():
			log.Println("DISCONNECTING")
			fmt.Printf("Stopping connection for filter: %s\n", filterRecord.Id)
			return nil
		default:

			msg, err := sourceMQConnector.ReceiveMessage()
			if err != nil {
				return fmt.Errorf("Failed to receive message: %v", err)
			}

			format, err := DetectFormat(msg)
			if err != nil {
				log.Println("unknown format")
				continue
			}

			var response []byte
			if format == "XML" {
				// Handle XML
				doc := etree.NewDocument()
				if err := doc.ReadFromString(string(msg)); err != nil {
					log.Printf("Error in reading the XML message: %v", err)
					continue
				}
				namespace := doc.Root().Space

				for _, p := range filterPaths {
					RemoveElements(doc.Root(), namespace, p)
					itemsElement := FindElement(doc.Root(), namespace, p)
					if itemsElement != nil {
						itemElement := FindElement(itemsElement, namespace, p)
						if itemElement != nil {
							fmt.Printf("Found item: %s\n", itemElement.Text())
						} else {
							fmt.Println("Item element not found")
						}
					} else {
						fmt.Println("Items element not found")
					}
				}

				result, err := doc.WriteToString()
				if err != nil {
					panic(err)
				}
				response = []byte(result)
			} else if format == "JSON" {
				// Handle JSON
				filteredJson, err := RemoveJSONPaths(msg, filterPaths)
				if err != nil {
					log.Printf("error reading the JSON message: %v", err)
					continue
				}
				response = filteredJson
			}

			fmt.Printf("Received message:\n%s\n", string(msg))

			err = destinationMQConnector.Connect()
			if err != nil {
				return fmt.Errorf("Failed to connect to MQ: %v", err)
			}
			defer destinationMQConnector.Disconnect()

			if response != nil {
				err = destinationMQConnector.SendMessage(response)
				if err != nil {
					log.Println("Failed to send message: %v", err)
					continue
				}
				fmt.Printf("Sent message:\n%s\n", string(response))
			}
		}
	}
}

// Recursive function to extract all tags from an XML element
func ExtractTags(element *etree.Element, tagSet map[string]struct{}) {
	if element == nil {
		return
	}
	tagWithNamespace := element.Tag
	tagSet[tagWithNamespace] = struct{}{}
	for _, child := range element.ChildElements() {
		ExtractTags(child, tagSet)
	}
}

// Function to handle the recursive mapping of nodes.
func nodeToMap(nodes []models.Node, path string, config map[string]bool) {
	for _, node := range nodes {
		currentPath := path
		if path != "" {
			currentPath += "."
		}
		currentPath += node.XMLName.Local

		// Assume each node is enabled; this flag can later be toggled as needed.
		config[currentPath] = true

		// Recursively handle child nodes.
		if len(node.Nodes) > 0 {
			nodeToMap(node.Nodes, currentPath, config)
		}
	}
}

func appendConfigPaths(config map[string]bool) (pathConfig []models.ConfigEntry) {

	for path, enabled := range config {
		data := models.ConfigEntry{
			FieldPath: path,
			Enabled:   enabled,
		}
		pathConfig = append(pathConfig, data)

	}
	return pathConfig
}

func ExtractPaths(fileBytes []byte) []models.ConfigEntry {
	// Parse XML
	var nodes models.Node
	if err := xml.Unmarshal(fileBytes, &nodes); err != nil {
		panic(err)
	}

	// Initialize the configuration map
	config := make(map[string]bool)
	if len(nodes.Nodes) > 0 {
		// Convert XML nodes to a configuration map
		nodeToMap(nodes.Nodes, "", config)

	}

	// Insert configuration into PocketBase
	configPaths := appendConfigPaths(config)

	return configPaths
}

func GetRootTag(xmlBytes []byte) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(xmlBytes)))

	// Iterate through the tokens of the XML input
	for {
		t, err := decoder.Token()
		if err == io.EOF {
			break // End of file, stop reading
		}
		if err != nil {
			return "", err // Handle the error from decoding
		}

		switch se := t.(type) {
		case xml.StartElement:
			return se.Name.Local, nil // Return the name of the first start element
		}
	}

	return "", fmt.Errorf("no root element found")
}

func RemoveJSONPaths(originalJSON []byte, pathsToRemove []string) ([]byte, error) {
	// Unmarshal the JSON into a map
	var data map[string]interface{}
	err := json.Unmarshal(originalJSON, &data)
	if err != nil {
		return nil, err
	}

	// Iterate over all paths to remove
	for _, path := range pathsToRemove {
		keys := strings.Split(path, ".")
		deletePath(&data, keys)
	}

	// Marshal the modified map back into JSON
	modifiedJSON, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	return modifiedJSON, nil
}

// deletePath deletes the specified path from the data map
func deletePath(data *map[string]interface{}, keys []string) {
	current := *data
	for i, key := range keys {
		if i == len(keys)-1 {
			delete(current, key)
		} else {
			if next, ok := current[key].(map[string]interface{}); ok {
				current = next
			} else {
				// Path is invalid
				return
			}
		}
	}
}

// detectFormat takes a []byte message and returns whether it is XML or JSON.
func DetectFormat(message []byte) (string, error) {
	if IsJSON(message) {
		return "JSON", nil
	} else if IsXML(message) {
		return "XML", nil
	} else {
		return "", fmt.Errorf("unknown format")
	}
}

// isJSON checks if the message is JSON.
func IsJSON(message []byte) bool {
	var js json.RawMessage
	return json.Unmarshal(message, &js) == nil
}

// isXML checks if the message is XML.
func IsXML(message []byte) bool {
	return xml.Unmarshal(message, new(interface{})) == nil
}

func RemoveElements(elem *etree.Element, namespace, tag string) {
	for _, child := range elem.ChildElements() {
		if child.Space == namespace && child.Tag == tag {
			elem.RemoveChild(child)
		} else {
			RemoveElements(child, namespace, tag)
		}
	}
	// Remove whitespace-only text nodes
	for _, child := range elem.Child {
		if c, ok := child.(*etree.CharData); ok && strings.TrimSpace(c.Data) == "" {
			elem.RemoveChild(child)
		}
	}
}

func RemoveEmptyLines(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// findElement recursively finds the first element with the given namespace and tag
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
