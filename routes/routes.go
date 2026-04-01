package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mqConnector/Data"
	tools "mqConnector/Tools"
	"mqConnector/dlq"
	"mqConnector/health"
	"mqConnector/metrics"
	"mqConnector/models"
	"net/http"
	"strconv"
	"strings"

	"github.com/beevik/etree"
	"github.com/clbanning/mxj/v2"
	pbmodels "github.com/pocketbase/pocketbase/models"

	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

const maxUploadSize = 10 * 1024 * 1024 // 10MB

var (
	globalCtx      context.Context
	globalCancel   context.CancelFunc
	activeRoutines = make(map[string]context.CancelFunc)
)

// InitRoutes registers all routes and hooks. Returns the global cancel function for graceful shutdown.
func InitRoutes(app *pocketbase.PocketBase) context.CancelFunc {
	globalCtx, globalCancel = context.WithCancel(context.Background())

	// On serve: load all MQ_FILTERS and start processing routines
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		globalCancel()
		globalCtx, globalCancel = context.WithCancel(context.Background())
		activeRoutines = make(map[string]context.CancelFunc)

		if err := startAllFilterRoutines(app); err != nil {
			log.Printf("Error starting filter routines: %v", err)
		}
		return nil
	})

	// On serve: load template collections into memory cache
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		collections, err := app.Dao().FindCollectionsByType(pbmodels.CollectionTypeBase)
		if err != nil {
			return err
		}
		collectionsMap := make(map[string][]models.ConfigEntry)
		for _, col := range collections {
			if col.Name != "mq_config" && !strings.Contains(col.Name, "MQ") && !strings.Contains(col.Name, "config") && !strings.Contains(col.Name, "DLQ") {
				var configPaths []models.ConfigEntry
				err := app.DB().Select("FieldPath").From(col.Name).All(&configPaths)
				if err != nil {
					continue
				}
				collectionsMap[col.Name] = configPaths
			}
		}
		Data.InitCollections(collectionsMap)
		return nil
	})

	// On serve: register all HTTP routes
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		registerFilterRoute(app, e)
		registerUploadRoutes(app, e)
		registerDLQRoutes(app, e)
		registerMetricsRoutes(app, e)
		registerHealthRoute(app, e)
		InitBridgeRoutes(app, e)
		return nil
	})

	// On collection delete: clean up in-memory cache
	app.OnCollectionAfterDeleteRequest().Add(func(e *core.CollectionDeleteEvent) error {
		return Data.DeleteCollection(e.Collection.Name)
	})

	// On MQ_FILTERS update: restart all routines with new config
	app.OnRecordAfterUpdateRequest("MQ_FILTERS").Add(func(e *core.RecordUpdateEvent) error {
		globalCancel()
		globalCtx, globalCancel = context.WithCancel(context.Background())
		activeRoutines = make(map[string]context.CancelFunc)

		if err := startAllFilterRoutines(app); err != nil {
			log.Printf("Error restarting filter routines: %v", err)
		}
		return nil
	})

	return globalCancel
}

// startAllFilterRoutines loads all MQ_FILTERS and starts a goroutine for each.
func startAllFilterRoutines(app *pocketbase.PocketBase) error {
	filterCollection, err := app.Dao().FindCollectionByNameOrId("MQ_FILTERS")
	if err != nil {
		return err
	}

	filterRecords := []*pbmodels.Record{}
	if err := app.Dao().RecordQuery(filterCollection.Name).All(&filterRecords); err != nil {
		return err
	}

	app.Dao().ExpandRecords(filterRecords, []string{"FieldPath", "source", "destination"}, nil)

	for _, filterRecord := range filterRecords {
		config, err := buildRoutineConfig(app, filterRecord)
		if err != nil {
			log.Printf("Error building config for filter %s: %v", filterRecord.Id, err)
			continue
		}

		ctx, cancel := context.WithCancel(globalCtx)
		activeRoutines[filterRecord.Id] = cancel

		go func(ctx context.Context, config models.RoutineConfig) {
			if err := tools.StartRoutine(ctx, config); err != nil {
				log.Printf("Error in routine for filter %s: %v", config.FilterRecord.Id, err)
				metrics.GetStore().SetStatus(config.FilterRecord.Id, "error", err.Error())
			}
		}(ctx, config)
	}

	return nil
}

// buildRoutineConfig constructs a full RoutineConfig from a filter record.
func buildRoutineConfig(app *pocketbase.PocketBase, filterRecord *pbmodels.Record) (models.RoutineConfig, error) {
	sourceConn, destConn, filterPaths, err := GetFilterEntities(app, filterRecord)
	if err != nil {
		return models.RoutineConfig{}, err
	}

	outputFormat := filterRecord.GetString("outputFormat")
	if outputFormat == "" {
		outputFormat = "same"
	}

	// Load transforms
	transforms, err := loadTransforms(app, filterRecord.Id)
	if err != nil {
		log.Printf("Warning: failed to load transforms for filter %s: %v", filterRecord.Id, err)
	}

	// Load routing rules
	routingRules, err := loadRoutingRules(app, filterRecord.Id)
	if err != nil {
		log.Printf("Warning: failed to load routing rules for filter %s: %v", filterRecord.Id, err)
	}

	// Load pipeline stages
	stages, err := loadPipelineStages(app, filterRecord.Id)
	if err != nil {
		log.Printf("Warning: failed to load pipeline stages for filter %s: %v", filterRecord.Id, err)
	}

	// Load schema if configured
	schemaType, schemaContent := loadSchema(app, filterRecord)

	// DLQ callback
	dlqFunc := func(msg []byte, reason string) {
		dlq.SendToDLQ(app, dlq.DLQEntry{
			OriginalMessage: string(msg),
			ErrorReason:     reason,
			FilterID:        filterRecord.Id,
			SourceQueue:     sourceConn["queueName"],
		})
	}

	return models.RoutineConfig{
		FilterPaths:   filterPaths,
		SourceConn:    sourceConn,
		DefaultDest:   destConn,
		OutputFormat:  outputFormat,
		Transforms:    transforms,
		RoutingRules:  routingRules,
		Stages:        stages,
		SchemaType:    schemaType,
		SchemaContent: schemaContent,
		DLQFunc:       dlqFunc,
		FilterRecord:  filterRecord,
	}, nil
}

// loadTransforms queries MQ_TRANSFORMS for a given filter ID.
func loadTransforms(app *pocketbase.PocketBase, filterID string) ([]models.TransformRule, error) {
	collection, err := app.Dao().FindCollectionByNameOrId("MQ_TRANSFORMS")
	if err != nil {
		return nil, nil // collection doesn't exist yet
	}

	records := []*pbmodels.Record{}
	err = app.Dao().RecordQuery(collection.Name).
		AndWhere(app.Dao().DB().NewExp("filter_id = {:fid}", map[string]any{"fid": filterID})).
		OrderBy("order ASC").
		All(&records)
	if err != nil {
		return nil, err
	}

	var rules []models.TransformRule
	for _, r := range records {
		rules = append(rules, models.TransformRule{
			TransformType: r.GetString("transform_type"),
			SourcePath:    r.GetString("source_path"),
			TargetPath:    r.GetString("target_path"),
			MaskPattern:   r.GetString("mask_pattern"),
			MaskReplace:   r.GetString("mask_replacement"),
			Order:         r.GetInt("order"),
		})
	}
	return rules, nil
}

// loadRoutingRules queries MQ_ROUTING_RULES for a given filter ID.
func loadRoutingRules(app *pocketbase.PocketBase, filterID string) ([]models.RoutingRule, error) {
	collection, err := app.Dao().FindCollectionByNameOrId("MQ_ROUTING_RULES")
	if err != nil {
		return nil, nil
	}

	records := []*pbmodels.Record{}
	err = app.Dao().RecordQuery(collection.Name).
		AndWhere(app.Dao().DB().NewExp("filter_id = {:fid}", map[string]any{"fid": filterID})).
		OrderBy("priority ASC").
		All(&records)
	if err != nil {
		return nil, err
	}

	var rules []models.RoutingRule
	for _, r := range records {
		app.Dao().ExpandRecord(r, []string{"destination"}, nil)
		destRecord, ok := r.Expand()["destination"].(*pbmodels.Record)
		if !ok {
			continue
		}

		// Build dest connection config
		app.Dao().ExpandRecord(destRecord, []string{"type"}, nil)
		destType := destRecord.Expand()
		typeRecord, ok := destType["type"].(*pbmodels.Record)
		if !ok {
			continue
		}

		destConn := map[string]string{
			"type":         typeRecord.GetString("TYPE"),
			"queueManager": destRecord.GetString("queueManager"),
			"connName":     destRecord.GetString("connName"),
			"channel":      destRecord.GetString("channel"),
			"user":         destRecord.GetString("user"),
			"password":     destRecord.GetString("password"),
			"queueName":    destRecord.GetString("queueName"),
			"url":          destRecord.GetString("url"),
			"brokers":      destRecord.GetString("brokers"),
			"topic":        destRecord.GetString("topic"),
		}

		rules = append(rules, models.RoutingRule{
			ConditionPath:     r.GetString("condition_path"),
			ConditionOperator: r.GetString("condition_operator"),
			ConditionValue:    r.GetString("condition_value"),
			DestConn:          destConn,
			Priority:          r.GetInt("priority"),
			Enabled:           r.GetBool("enabled"),
		})
	}
	return rules, nil
}

// loadPipelineStages queries MQ_PIPELINE_STAGES for a given filter ID.
func loadPipelineStages(app *pocketbase.PocketBase, filterID string) ([]models.PipelineStage, error) {
	collection, err := app.Dao().FindCollectionByNameOrId("MQ_PIPELINE_STAGES")
	if err != nil {
		return nil, nil
	}

	records := []*pbmodels.Record{}
	err = app.Dao().RecordQuery(collection.Name).
		AndWhere(app.Dao().DB().NewExp("filter_id = {:fid}", map[string]any{"fid": filterID})).
		OrderBy("stage_order ASC").
		All(&records)
	if err != nil {
		return nil, err
	}

	var stages []models.PipelineStage
	for _, r := range records {
		configJSON := ""
		if v := r.Get("stage_config"); v != nil {
			if b, err := json.Marshal(v); err == nil {
				configJSON = string(b)
			}
		}
		stages = append(stages, models.PipelineStage{
			StageOrder: r.GetInt("stage_order"),
			StageType:  r.GetString("stage_type"),
			Config:     configJSON,
			Enabled:    r.GetBool("enabled"),
		})
	}
	return stages, nil
}

// loadSchema loads the schema validation config for a filter record.
func loadSchema(app *pocketbase.PocketBase, filterRecord *pbmodels.Record) (string, string) {
	// Schema is not a standard field yet — check if the filter has a schema_id or similar
	// For now, return empty (no validation) unless the schema relation exists
	schemaID := filterRecord.GetString("schema")
	if schemaID == "" {
		return "", ""
	}

	schemaRecord, err := app.Dao().FindRecordById("MQ_SCHEMAS", schemaID)
	if err != nil {
		return "", ""
	}

	return schemaRecord.GetString("schema_type"), schemaRecord.GetString("schema_content")
}

// registerFilterRoute registers POST /api/filter
func registerFilterRoute(app *pocketbase.PocketBase, e *core.ServeEvent) {
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/filter",
		Handler: func(c echo.Context) error {
			file, err := c.FormFile("file")
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "Failed to get file"})
			}
			if file.Size > maxUploadSize {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "File too large (max 10MB)"})
			}
			src, err := file.Open()
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to open file"})
			}
			defer src.Close()
			xmlData, err := io.ReadAll(src)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to read file"})
			}
			mv, err := mxj.NewMapXml(xmlData)
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid XML"})
			}
			jsonData, err := json.Marshal(mv)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert to JSON"})
			}
			rootTag, err := tools.GetRootTag(xmlData)
			if err != nil {
				return c.String(http.StatusInternalServerError, err.Error())
			}
			paths, err := Data.GetCollectionByName(rootTag)
			if err != nil {
				return c.String(http.StatusInternalServerError, err.Error())
			}
			var arrayPaths []string
			for _, p := range paths {
				arrayPaths = append(arrayPaths, rootTag+"."+p.FieldPath)
			}
			filteredJson, err := tools.RemoveJSONPaths(jsonData, arrayPaths)
			if err != nil {
				return c.String(http.StatusInternalServerError, err.Error())
			}
			var processedData map[string]interface{}
			if err := json.Unmarshal(filteredJson, &processedData); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert JSON to map"})
			}
			xmlResponse, err := mxj.Map(processedData).Xml()
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert map to XML"})
			}
			return c.XMLBlob(http.StatusOK, xmlResponse)
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})
}

// registerUploadRoutes registers POST /api/upload/sample and /api/v2/upload/sample
func registerUploadRoutes(app *pocketbase.PocketBase, e *core.ServeEvent) {
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/v2/upload/sample",
		Handler: func(c echo.Context) error {
			file, err := c.FormFile("file")
			if err != nil {
				return c.String(http.StatusBadRequest, "Failed to read file from request")
			}
			if file.Size > maxUploadSize {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "File too large (max 10MB)"})
			}

			src, err := file.Open()
			if err != nil {
				return err
			}
			defer src.Close()

			buf := new(bytes.Buffer)
			buf.ReadFrom(src)

			doc := etree.NewDocument()
			if err := doc.ReadFromBytes(buf.Bytes()); err != nil {
				return c.String(http.StatusBadRequest, "Failed to parse XML file")
			}

			namespace := doc.Root().Space
			rootTag := doc.Root().Tag
			format, err := tools.DetectFormat(buf.Bytes())
			if err != nil {
				return c.String(http.StatusInternalServerError, err.Error())
			}

			tagSet := make(map[string]struct{})
			tools.ExtractTags(doc.Root(), tagSet)

			tags := make([]string, 0, len(tagSet))
			for tag := range tagSet {
				tags = append(tags, tag)
			}

			collection, err := app.Dao().FindCollectionByNameOrId("Templates")
			if err != nil {
				return err
			}

			for _, config := range tags {
				record := pbmodels.NewRecord(collection)
				record.Set("FieldPath", config)
				record.Set("NameSpace", namespace)
				record.Set("T_TYPE", format)
				record.Set("T_NAME", rootTag)
				if err := app.Dao().SaveRecord(record); err != nil {
					return err
				}
			}

			return c.JSON(http.StatusOK, tags)
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})

	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/upload/sample",
		Handler: func(c echo.Context) error {
			file, err := c.FormFile("file")
			if err != nil {
				return err
			}
			if file.Size > maxUploadSize {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "File too large (max 10MB)"})
			}

			src, err := file.Open()
			if err != nil {
				return err
			}
			defer src.Close()

			fileBytes, err := io.ReadAll(src)
			if err != nil {
				return err
			}

			format, err := tools.DetectFormat(fileBytes)
			if err != nil {
				return c.String(http.StatusBadRequest, "unknown format")
			}

			rootTag, err := tools.GetRootTag(fileBytes)
			if err != nil {
				return err
			}

			collection, err := app.Dao().FindCollectionByNameOrId("Templates")
			if err != nil {
				return err
			}
			paths := tools.ExtractPaths(fileBytes)

			for _, config := range paths {
				record := pbmodels.NewRecord(collection)
				record.Set("FieldPath", rootTag+"."+config.FieldPath)
				record.Set("T_TYPE", format)
				record.Set("T_NAME", rootTag)
				if err := app.Dao().SaveRecord(record); err != nil {
					return err
				}
			}

			err = Data.AddCollection(app, rootTag)
			if err != nil {
				return err
			}
			return c.String(http.StatusOK, "inserted")
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
			apis.ActivityLogger(app),
		},
	})
}

// registerDLQRoutes registers DLQ management endpoints.
func registerDLQRoutes(app *pocketbase.PocketBase, e *core.ServeEvent) {
	// GET /api/dlq — list dead letters
	e.Router.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/api/dlq",
		Handler: func(c echo.Context) error {
			page, _ := strconv.Atoi(c.QueryParam("page"))
			if page < 1 {
				page = 1
			}
			perPage, _ := strconv.Atoi(c.QueryParam("perPage"))
			if perPage < 1 {
				perPage = 20
			}

			records, total, err := dlq.ListMessages(app, page, perPage)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}

			return c.JSON(http.StatusOK, map[string]interface{}{
				"page":    page,
				"perPage": perPage,
				"total":   total,
				"items":   records,
			})
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})

	// POST /api/dlq/:id/retry — retry a dead letter
	e.Router.AddRoute(echo.Route{
		Method: http.MethodPost,
		Path:   "/api/dlq/:id/retry",
		Handler: func(c echo.Context) error {
			id := c.PathParam("id")

			record, err := dlq.GetMessage(app, id)
			if err != nil {
				return c.JSON(http.StatusNotFound, map[string]string{"error": "DLQ entry not found"})
			}

			retryCount := record.GetInt("retry_count")
			if retryCount >= dlq.MaxRetries() {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "Max retries exceeded"})
			}

			if err := dlq.IncrementRetryCount(app, id); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}

			return c.JSON(http.StatusOK, map[string]string{
				"status":      "retry_queued",
				"retry_count": fmt.Sprintf("%d", retryCount+1),
			})
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})

	// DELETE /api/dlq/:id — delete a dead letter
	e.Router.AddRoute(echo.Route{
		Method: http.MethodDelete,
		Path:   "/api/dlq/:id",
		Handler: func(c echo.Context) error {
			id := c.PathParam("id")
			if err := dlq.DeleteMessage(app, id); err != nil {
				return c.JSON(http.StatusNotFound, map[string]string{"error": "DLQ entry not found"})
			}
			return c.JSON(http.StatusOK, map[string]string{"status": "deleted"})
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})
}

// registerMetricsRoutes registers metrics endpoints.
func registerMetricsRoutes(app *pocketbase.PocketBase, e *core.ServeEvent) {
	// GET /api/metrics — JSON metrics
	e.Router.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/api/metrics",
		Handler: func(c echo.Context) error {
			store := metrics.GetStore()
			return c.JSON(http.StatusOK, map[string]interface{}{
				"uptime":      store.GetUptime().String(),
				"connections": store.GetAll(),
			})
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})

	// GET /api/metrics/prometheus — Prometheus format
	e.Router.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/api/metrics/prometheus",
		Handler: func(c echo.Context) error {
			return c.String(http.StatusOK, metrics.GetStore().FormatPrometheus())
		},
		Middlewares: []echo.MiddlewareFunc{
			apis.RequireAdminAuth(),
		},
	})
}

// registerHealthRoute registers the health check endpoint.
func registerHealthRoute(app *pocketbase.PocketBase, e *core.ServeEvent) {
	e.Router.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/api/health",
		Handler: func(c echo.Context) error {
			status := health.Check(app)

			// If no auth header, return minimal response
			adminAuth := c.Request().Header.Get("Authorization")
			if adminAuth == "" {
				return c.JSON(http.StatusOK, map[string]string{"status": status.Status})
			}

			return c.JSON(http.StatusOK, status)
		},
	})
}

// GetFilterEntities extracts source/destination connection configs and filter paths from an expanded filter record.
func GetFilterEntities(app *pocketbase.PocketBase, record *pbmodels.Record) (s map[string]string, d map[string]string, p []string, err error) {
	expandedRecord := record.Expand()
	source, ok := expandedRecord["source"].(*pbmodels.Record)
	if !ok {
		return nil, nil, nil, fmt.Errorf("source connection not loaded for filter %s", record.Id)
	}
	destination, ok := expandedRecord["destination"].(*pbmodels.Record)
	if !ok {
		return nil, nil, nil, fmt.Errorf("destination connection not loaded for filter %s", record.Id)
	}

	paths, ok := expandedRecord["FieldPath"].([]*pbmodels.Record)
	if !ok {
		log.Println("Paths are not loaded or empty")
	}

	var filterPaths []string
	for _, path := range paths {
		filterPaths = append(filterPaths, path.GetString("FieldPath"))
	}

	sourceConn := map[string]string{
		"queueManager": source.GetString("queueManager"),
		"connName":     source.GetString("connName"),
		"channel":      source.GetString("channel"),
		"user":         source.GetString("user"),
		"password":     source.GetString("password"),
		"queueName":    source.GetString("queueName"),
		"url":          source.GetString("url"),
		"brokers":      source.GetString("brokers"),
		"topic":        source.GetString("topic"),
	}

	app.Dao().ExpandRecord(source, []string{"type"}, nil)
	sourceType := source.Expand()
	sourceConnectionType, ok := sourceType["type"].(*pbmodels.Record)
	if !ok {
		return nil, nil, nil, fmt.Errorf("source connection type not loaded: %v", sourceType["type"])
	}
	sourceConn["type"] = sourceConnectionType.GetString("TYPE")

	destConn := map[string]string{
		"queueManager": destination.GetString("queueManager"),
		"connName":     destination.GetString("connName"),
		"channel":      destination.GetString("channel"),
		"user":         destination.GetString("user"),
		"password":     destination.GetString("password"),
		"queueName":    destination.GetString("queueName"),
		"url":          destination.GetString("url"),
		"brokers":      destination.GetString("brokers"),
		"topic":        destination.GetString("topic"),
	}

	app.Dao().ExpandRecord(destination, []string{"type"}, nil)
	destType := destination.Expand()
	destConnectionType, ok := destType["type"].(*pbmodels.Record)
	if !ok {
		return nil, nil, nil, fmt.Errorf("destination connection type not loaded: %v", destType["type"])
	}
	destConn["type"] = destConnectionType.GetString("TYPE")

	return sourceConn, destConn, filterPaths, nil
}
