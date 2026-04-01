package routes

import (
	"fmt"
	"io"
	"mqConnector/mq"
	tools "mqConnector/Tools"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	pbmodels "github.com/pocketbase/pocketbase/models"
)

const maxBridgeBodySize = 10 * 1024 * 1024 // 10MB

// InitBridgeRoutes registers REST<->MQ bridge endpoints.
func InitBridgeRoutes(app *pocketbase.PocketBase, e *core.ServeEvent) {
	// POST /api/bridge/publish/:connectionId — publish a message to a queue
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/bridge/publish/:connectionId",
		Handler: func(c echo.Context) error {
			connectionId := c.PathParam("connectionId")

			config, queueType, err := buildMQConfigFromRecord(app, connectionId)
			if err != nil {
				return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("Connection not found: %v", err)})
			}

			// Read request body with size limit
			body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxBridgeBodySize))
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "Failed to read request body"})
			}
			if len(body) == 0 {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "Empty message body"})
			}

			connector, err := mq.Pool.Get(connectionId+"-pub", queueType, config)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to connect: %v", err)})
			}

			if err := connector.SendMessage(body); err != nil {
				mq.Pool.Release(connectionId + "-pub")
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to send message: %v", err)})
			}

			return c.JSON(http.StatusOK, map[string]string{"status": "published", "bytes": fmt.Sprintf("%d", len(body))})
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})

	// POST /api/bridge/consume/:connectionId — consume a message from a queue
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/bridge/consume/:connectionId",
		Handler: func(c echo.Context) error {
			connectionId := c.PathParam("connectionId")

			config, queueType, err := buildMQConfigFromRecord(app, connectionId)
			if err != nil {
				return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("Connection not found: %v", err)})
			}

			connector, err := mq.Pool.Get(connectionId+"-con", queueType, config)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to connect: %v", err)})
			}

			msg, err := connector.ReceiveMessage()
			if err != nil {
				mq.Pool.Release(connectionId + "-con")
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to receive message: %v", err)})
			}

			// Detect format and set Content-Type
			format, _ := tools.DetectFormat(msg)
			switch format {
			case "JSON":
				return c.Blob(http.StatusOK, "application/json", msg)
			case "XML":
				return c.Blob(http.StatusOK, "application/xml", msg)
			default:
				return c.Blob(http.StatusOK, "application/octet-stream", msg)
			}
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})
}

// buildMQConfigFromRecord retrieves an MQS record and builds a connection config map.
func buildMQConfigFromRecord(app *pocketbase.PocketBase, connectionId string) (map[string]string, mq.QueueType, error) {
	record, err := app.Dao().FindRecordById("MQS", connectionId)
	if err != nil {
		return nil, 0, err
	}

	app.Dao().ExpandRecord(record, []string{"type"}, nil)
	typeExpanded := record.Expand()
	typeRecord, ok := typeExpanded["type"].(*pbmodels.Record)
	if !ok {
		return nil, 0, fmt.Errorf("failed to resolve MQ type for connection %s", connectionId)
	}

	typeStr := typeRecord.GetString("TYPE")
	queueType := mq.GetQueueType(typeStr)

	config := map[string]string{
		"queueManager": record.GetString("queueManager"),
		"connName":     record.GetString("connName"),
		"channel":      record.GetString("channel"),
		"user":         record.GetString("user"),
		"password":     record.GetString("password"),
		"queueName":    record.GetString("queueName"),
		"url":          record.GetString("url"),
		"brokers":      record.GetString("brokers"),
		"topic":        record.GetString("topic"),
	}

	return config, queueType, nil
}
