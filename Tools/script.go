package tools

import (
	"encoding/json"
	"fmt"
)

// ScriptStage executes a user-defined JavaScript transformation on the message.
// The script receives the message as a JSON object in the `msg` variable
// and must return the modified object.
//
// Example script:
//
//	msg.timestamp = Date.now();
//	msg.total = msg.price * msg.quantity;
//	delete msg.internal_notes;
//	msg;
type ScriptStage struct {
	Script string
}

// Execute runs the JavaScript script on the message.
// For XML messages, the message is converted to JSON first, processed, then converted back.
func (s *ScriptStage) Execute(message []byte, format string) ([]byte, string, error) {
	if s.Script == "" {
		return message, format, nil
	}

	// Convert to JSON map for script processing
	var data interface{}
	originalFormat := format

	if format == "XML" {
		// Convert XML to JSON for script processing
		jsonMsg, err := TranslateFormat(message, "XML", "JSON")
		if err != nil {
			return nil, format, fmt.Errorf("script: failed to convert XML to JSON: %v", err)
		}
		if err := json.Unmarshal(jsonMsg, &data); err != nil {
			return nil, format, fmt.Errorf("script: failed to parse converted JSON: %v", err)
		}
	} else if format == "JSON" {
		if err := json.Unmarshal(message, &data); err != nil {
			return nil, format, fmt.Errorf("script: failed to parse JSON: %v", err)
		}
	} else {
		return message, format, nil
	}

	// Execute script
	result, err := executeScript(s.Script, data)
	if err != nil {
		return nil, format, fmt.Errorf("script execution error: %v", err)
	}

	// Marshal result back
	jsonResult, err := json.Marshal(result)
	if err != nil {
		return nil, format, fmt.Errorf("script: failed to marshal result: %v", err)
	}

	// Convert back to original format if needed
	if originalFormat == "XML" {
		xmlResult, err := TranslateFormat(jsonResult, "JSON", "XML")
		if err != nil {
			return nil, format, fmt.Errorf("script: failed to convert result back to XML: %v", err)
		}
		return xmlResult, "XML", nil
	}

	return jsonResult, "JSON", nil
}

// executeScript runs a JavaScript script with the given data as the `msg` variable.
// Uses a simple expression evaluator for basic operations.
// For full JS support, integrate github.com/dop251/goja when available.
func executeScript(script string, data interface{}) (interface{}, error) {
	// Built-in script engine using Go-native evaluation
	// Supports a subset of operations via a simple interpreter
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return data, nil
	}

	result, err := evalScript(script, dataMap)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// evalScript is a lightweight script evaluator that supports common operations.
// It processes line-by-line commands on the msg object.
//
// Supported operations:
//   - delete msg.field.path        -> removes a field
//   - msg.field = "value"          -> sets a string value
//   - msg.field = 123              -> sets a numeric value
//   - msg.field = true/false       -> sets a boolean value
//   - msg.field = msg.other        -> copies a field value
//   - msg.new = msg.a + msg.b      -> adds numeric fields
//   - msg.new = msg.a * msg.b      -> multiplies numeric fields
//   - msg                          -> returns the message (last expression)
func evalScript(script string, data map[string]interface{}) (interface{}, error) {
	lines := splitScriptLines(script)

	for _, line := range lines {
		line = trimLine(line)
		if line == "" || line == "msg" || line == "msg;" {
			continue
		}

		var err error
		if isDeleteOp(line) {
			err = evalDelete(line, data)
		} else if isAssignOp(line) {
			err = evalAssign(line, data)
		}
		if err != nil {
			return nil, fmt.Errorf("line '%s': %v", line, err)
		}
	}

	return data, nil
}
