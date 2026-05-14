package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ScriptStage executes a small line-oriented script over a structured message.
// The intent is to allow operators to express common transforms without
// bringing in a full JS runtime. Supported operations:
//
//   - delete msg.field.path             → removes a field
//   - msg.field = "value"               → set string
//   - msg.field = 123                   → set number
//   - msg.field = true / false          → set bool
//   - msg.field = msg.other             → copy field
//   - msg.field = msg.a + msg.b         → numeric add or string concat
//   - msg.field = msg.a * msg.b         → numeric multiply (also - / )
//   - msg.field = Date.now()            → set Unix millis
//
// Anything else is silently ignored.
type ScriptStage struct {
	Script  string
	Timeout time.Duration // optional hard cap; 0 ⇒ inherit ctx
}

func (s *ScriptStage) Name() string { return "script" }

func (s *ScriptStage) Execute(ctx context.Context, message []byte, format Format) ([]byte, Format, *Result, error) {
	if strings.TrimSpace(s.Script) == "" {
		return message, format, nil, nil
	}

	if s.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Timeout)
		defer cancel()
	}

	originalFormat := format
	data, err := decodeStructured(message, format)
	if err != nil {
		return nil, format, nil, fmt.Errorf("script: %w", err)
	}
	if data == nil {
		// Non-structured payload — script cannot operate. Pass through.
		return message, format, nil, nil
	}

	// Check timeout before running.
	select {
	case <-ctx.Done():
		return nil, format, nil, fmt.Errorf("script: %w", ctx.Err())
	default:
	}

	if err := runScript(ctx, s.Script, data); err != nil {
		return nil, format, nil, fmt.Errorf("script: %w", err)
	}

	out, err := encodeStructured(data, originalFormat)
	if err != nil {
		return nil, format, nil, fmt.Errorf("script: encode: %w", err)
	}
	return out, originalFormat, nil, nil
}

func runScript(ctx context.Context, script string, data map[string]any) error {
	for _, raw := range splitScriptLines(script) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := trimLine(raw)
		if line == "" || line == "msg" || line == "msg;" || strings.HasPrefix(line, "//") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "delete "):
			if err := evalDelete(line, data); err != nil {
				return err
			}
		case strings.Contains(line, "="):
			if err := evalAssign(line, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func splitScriptLines(script string) []string {
	var out []string
	for _, line := range strings.Split(script, "\n") {
		for _, part := range strings.Split(line, ";") {
			t := strings.TrimSpace(part)
			if t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

func trimLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ";")
	return strings.TrimSpace(s)
}

func evalDelete(line string, data map[string]any) error {
	p := strings.TrimSpace(strings.TrimPrefix(line, "delete "))
	p = strings.TrimPrefix(p, "msg.")
	if p == "" || p == "msg" {
		return errors.New("delete: cannot remove root")
	}
	deleteNestedValue(data, p)
	return nil
}

func evalAssign(line string, data map[string]any) error {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return errors.New("invalid assignment")
	}
	lhs := strings.TrimSpace(parts[0])
	rhs := strings.TrimSpace(parts[1])
	targetPath := strings.TrimPrefix(lhs, "msg.")
	if targetPath == lhs {
		return nil // not msg.xxx — silently ignore
	}
	v, err := evalExpr(rhs, data)
	if err != nil {
		return err
	}
	return setNestedValue(data, targetPath, v)
}

func evalExpr(expr string, data map[string]any) (any, error) {
	expr = strings.TrimSpace(expr)
	for _, op := range []string{" + ", " - ", " * ", " / "} {
		if i := strings.Index(expr, op); i > 0 {
			lhs := expr[:i]
			rhs := expr[i+len(op):]
			lv, err := evalExpr(lhs, data)
			if err != nil {
				return nil, err
			}
			rv, err := evalExpr(rhs, data)
			if err != nil {
				return nil, err
			}
			lf, lok := toFloat(lv)
			rf, rok := toFloat(rv)
			if lok && rok {
				switch strings.TrimSpace(op) {
				case "+":
					return lf + rf, nil
				case "-":
					return lf - rf, nil
				case "*":
					return lf * rf, nil
				case "/":
					if rf == 0 {
						return nil, errors.New("script: division by zero")
					}
					return lf / rf, nil
				}
			}
			if strings.TrimSpace(op) == "+" {
				return fmt.Sprintf("%v%v", lv, rv), nil
			}
		}
	}

	switch {
	case (strings.HasPrefix(expr, `"`) && strings.HasSuffix(expr, `"`)) ||
		(strings.HasPrefix(expr, `'`) && strings.HasSuffix(expr, `'`)):
		return expr[1 : len(expr)-1], nil
	case expr == "true":
		return true, nil
	case expr == "false":
		return false, nil
	case expr == "null" || expr == "nil":
		return nil, nil
	case expr == "Date.now()":
		return float64(time.Now().UnixMilli()), nil
	case strings.HasPrefix(expr, "msg."):
		path := strings.TrimPrefix(expr, "msg.")
		v, err := getNestedValue(data, path)
		if err != nil {
			return nil, nil
		}
		return v, nil
	}
	if n, err := strconv.ParseFloat(expr, 64); err == nil {
		return n, nil
	}
	return expr, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
