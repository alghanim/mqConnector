package Data

import (
	"log"
	"os"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/models"
	"github.com/pocketbase/pocketbase/models/schema"
)

func StartDB() *pocketbase.PocketBase {
	dataDir := os.Getenv("MQ_DATA_DIR")
	if dataDir == "" {
		dataDir = "./Data/pb_data"
	}

	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: dataDir,
		DefaultDev:     false,
	})

	err := checkMainCollections(app)
	if err != nil {
		panic(err)
	}

	return app
}

func createMQConfigCollections(app *pocketbase.PocketBase) error {
	// Templates collection — admin-only access (nil rules)
	templateCollection := &models.Collection{
		Name: "Templates",
		Type: models.CollectionTypeBase,
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "T_NAME",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "T_TYPE",
				Type: schema.FieldTypeSelect,
				Options: schema.SelectOptions{
					Values:    []string{"XML", "JSON"},
					MaxSelect: 1,
				},
			},
			&schema.SchemaField{
				Name: "NameSpace",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name:        "FieldPath",
				Type:        schema.FieldTypeText,
				Presentable: true,
			},
		),
	}

	err := app.Dao().SaveCollection(templateCollection)
	if err != nil {
		return err
	}

	templateCollection, err = app.Dao().FindCollectionByNameOrId("Templates")
	if err != nil {
		return err
	}

	// MQ_TYPES collection
	mqTypeCollection := &models.Collection{
		Name: "MQ_TYPES",
		Type: models.CollectionTypeBase,
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "MQ_ID",
				Type: schema.FieldTypeNumber,
			},
			&schema.SchemaField{
				Name:        "TYPE",
				Type:        schema.FieldTypeText,
				Presentable: true,
			},
		),
	}

	err = app.Dao().SaveCollection(mqTypeCollection)
	if err != nil {
		return err
	}
	mqTypeCollection, err = app.Dao().FindCollectionByNameOrId("MQ_TYPES")
	if err != nil {
		return err
	}

	// Seed MQ types
	for _, mqType := range []struct {
		ID   int
		Name string
	}{
		{0, "IBM"},
		{1, "RabbitMQ"},
		{2, "Kafka"},
	} {
		record := models.NewRecord(mqTypeCollection)
		record.Set("MQ_ID", mqType.ID)
		record.Set("TYPE", mqType.Name)
		if err := app.Dao().SaveRecord(record); err != nil {
			return err
		}
	}

	// MQS collection — stores MQ connection configurations
	mqsCollection := &models.Collection{
		Name: "MQS",
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "type",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: mqTypeCollection.Id,
					MaxSelect:    types.Pointer(1),
				},
			},
			&schema.SchemaField{
				Name: "queueManager",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "connName",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "channel",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "user",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "password",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "queueName",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "url",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "brokers",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "topic",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name:        "ownerName",
				Type:        schema.FieldTypeText,
				Presentable: true,
			},
		),
	}

	err = app.Dao().SaveCollection(mqsCollection)
	if err != nil {
		log.Println(err)
	}
	mqsCollection, err = app.Dao().FindCollectionByNameOrId("MQS")
	if err != nil {
		return err
	}

	// MQ_FILTERS collection — links source, destination, field paths, output format, and schema
	mqFiltersCollection := &models.Collection{
		Name: "MQ_FILTERS",
		Type: models.CollectionTypeBase,
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "source",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: mqsCollection.Id,
					MaxSelect:    types.Pointer(1),
				},
			},
			&schema.SchemaField{
				Name: "destination",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: mqsCollection.Id,
					MaxSelect:    types.Pointer(1),
				},
			},
			&schema.SchemaField{
				Name: "FieldPath",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: templateCollection.Id,
					MaxSelect:    nil,
				},
			},
			&schema.SchemaField{
				Name: "outputFormat",
				Type: schema.FieldTypeSelect,
				Options: schema.SelectOptions{
					Values:    []string{"same", "XML", "JSON"},
					MaxSelect: 1,
				},
			},
		),
	}

	err = app.Dao().SaveCollection(mqFiltersCollection)
	if err != nil {
		return err
	}

	mqFiltersCollection, err = app.Dao().FindCollectionByNameOrId("MQ_FILTERS")
	if err != nil {
		return err
	}

	// MQ_TRANSFORMS collection — field rename, mask, move rules
	mqTransformsCollection := &models.Collection{
		Name: "MQ_TRANSFORMS",
		Type: models.CollectionTypeBase,
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "filter_id",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: mqFiltersCollection.Id,
					MaxSelect:    types.Pointer(1),
				},
			},
			&schema.SchemaField{
				Name: "transform_type",
				Type: schema.FieldTypeSelect,
				Options: schema.SelectOptions{
					Values:    []string{"rename", "mask", "move"},
					MaxSelect: 1,
				},
			},
			&schema.SchemaField{
				Name: "source_path",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "target_path",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "mask_pattern",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "mask_replacement",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name:        "order",
				Type:        schema.FieldTypeNumber,
				Presentable: true,
			},
		),
	}

	err = app.Dao().SaveCollection(mqTransformsCollection)
	if err != nil {
		return err
	}

	// MQ_ROUTING_RULES collection — content-based routing rules
	mqRoutingRulesCollection := &models.Collection{
		Name: "MQ_ROUTING_RULES",
		Type: models.CollectionTypeBase,
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "filter_id",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: mqFiltersCollection.Id,
					MaxSelect:    types.Pointer(1),
				},
			},
			&schema.SchemaField{
				Name:        "condition_path",
				Type:        schema.FieldTypeText,
				Presentable: true,
			},
			&schema.SchemaField{
				Name: "condition_operator",
				Type: schema.FieldTypeSelect,
				Options: schema.SelectOptions{
					Values:    []string{"eq", "neq", "contains", "regex", "gt", "lt", "exists"},
					MaxSelect: 1,
				},
			},
			&schema.SchemaField{
				Name: "condition_value",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "destination",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: mqsCollection.Id,
					MaxSelect:    types.Pointer(1),
				},
			},
			&schema.SchemaField{
				Name: "priority",
				Type: schema.FieldTypeNumber,
			},
			&schema.SchemaField{
				Name: "enabled",
				Type: schema.FieldTypeBool,
			},
		),
	}

	err = app.Dao().SaveCollection(mqRoutingRulesCollection)
	if err != nil {
		return err
	}

	// MQ_PIPELINE_STAGES collection — ordered pipeline stages per filter
	mqPipelineStagesCollection := &models.Collection{
		Name: "MQ_PIPELINE_STAGES",
		Type: models.CollectionTypeBase,
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "filter_id",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: mqFiltersCollection.Id,
					MaxSelect:    types.Pointer(1),
				},
			},
			&schema.SchemaField{
				Name:        "stage_order",
				Type:        schema.FieldTypeNumber,
				Presentable: true,
			},
			&schema.SchemaField{
				Name: "stage_type",
				Type: schema.FieldTypeSelect,
				Options: schema.SelectOptions{
					Values:    []string{"filter", "transform", "route", "translate"},
					MaxSelect: 1,
				},
			},
			&schema.SchemaField{
				Name: "stage_config",
				Type: schema.FieldTypeJson,
			},
			&schema.SchemaField{
				Name: "enabled",
				Type: schema.FieldTypeBool,
			},
		),
	}

	err = app.Dao().SaveCollection(mqPipelineStagesCollection)
	if err != nil {
		return err
	}

	// DLQ collection — dead letter queue for failed messages
	dlqCollection := &models.Collection{
		Name: "DLQ",
		Type: models.CollectionTypeBase,
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "original_message",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name:        "error_reason",
				Type:        schema.FieldTypeText,
				Presentable: true,
			},
			&schema.SchemaField{
				Name: "filter_id",
				Type: schema.FieldTypeRelation,
				Options: schema.RelationOptions{
					CollectionId: mqFiltersCollection.Id,
					MaxSelect:    types.Pointer(1),
				},
			},
			&schema.SchemaField{
				Name: "source_queue",
				Type: schema.FieldTypeText,
			},
			&schema.SchemaField{
				Name: "retry_count",
				Type: schema.FieldTypeNumber,
			},
		),
	}

	err = app.Dao().SaveCollection(dlqCollection)
	if err != nil {
		return err
	}

	// MQ_SCHEMAS collection — JSON Schema / XSD validation schemas
	mqSchemasCollection := &models.Collection{
		Name: "MQ_SCHEMAS",
		Type: models.CollectionTypeBase,
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name:        "schema_name",
				Type:        schema.FieldTypeText,
				Presentable: true,
			},
			&schema.SchemaField{
				Name: "schema_type",
				Type: schema.FieldTypeSelect,
				Options: schema.SelectOptions{
					Values:    []string{"JSON_SCHEMA", "XSD"},
					MaxSelect: 1,
				},
			},
			&schema.SchemaField{
				Name: "schema_content",
				Type: schema.FieldTypeText,
			},
		),
	}

	err = app.Dao().SaveCollection(mqSchemasCollection)
	if err != nil {
		return err
	}

	return nil
}

func checkMainCollections(app *pocketbase.PocketBase) error {
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		_, err := app.Dao().FindCollectionByNameOrId("MQS")
		if err != nil {
			_ = createMQConfigCollections(app)
		}
		return nil
	})

	return nil
}
