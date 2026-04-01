package tools

import (
	"encoding/json"
	"fmt"

	"github.com/clbanning/mxj/v2"
)

// TranslateFormat converts a message between XML and JSON formats.
// If targetFormat is "same" or matches sourceFormat, the message is returned unchanged.
func TranslateFormat(message []byte, sourceFormat, targetFormat string) ([]byte, error) {
	if targetFormat == "" || targetFormat == "same" || targetFormat == sourceFormat {
		return message, nil
	}

	switch {
	case sourceFormat == "XML" && targetFormat == "JSON":
		return xmlToJSON(message)
	case sourceFormat == "JSON" && targetFormat == "XML":
		return jsonToXML(message)
	default:
		return nil, fmt.Errorf("unsupported translation: %s -> %s", sourceFormat, targetFormat)
	}
}

func xmlToJSON(xmlData []byte) ([]byte, error) {
	mv, err := mxj.NewMapXml(xmlData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse XML for translation: %v", err)
	}
	jsonData, err := json.Marshal(mv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON: %v", err)
	}
	return jsonData, nil
}

func jsonToXML(jsonData []byte) ([]byte, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, fmt.Errorf("failed to parse JSON for translation: %v", err)
	}
	xmlData, err := mxj.Map(data).Xml()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal XML: %v", err)
	}
	return xmlData, nil
}
