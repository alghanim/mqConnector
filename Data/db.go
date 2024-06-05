package Data

import (
	"log"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/models"
	"github.com/pocketbase/pocketbase/models/schema"
)

func StartDB() *pocketbase.PocketBase {

	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir: "./Data/pb_data",
		DefaultDev:     true,
	})

	err := checkMainCollections(app)
	if err != nil {
		panic(err)
	}

	return app
}

func createMQConfigCollections(app *pocketbase.PocketBase) error {

	templateCollection := &models.Collection{
		Name:       "Templates",
		Type:       models.CollectionTypeBase,
		CreateRule: types.Pointer(""),
		ViewRule:   types.Pointer(""),
		ListRule:   types.Pointer(""),
		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name: "T_NAME",
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

	templateCollection = nil
	templateCollection, err = app.Dao().FindCollectionByNameOrId("Templates")
	if err != nil {
		return err
	}

	mqTypeCollection := &models.Collection{
		Name:       "MQ_TYPES",
		Type:       models.CollectionTypeBase,
		CreateRule: types.Pointer(""),
		ViewRule:   types.Pointer(""),
		ListRule:   types.Pointer(""),
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
		panic(err)
	}
	mqTypeCollection = nil
	mqTypeCollection, err = app.Dao().FindCollectionByNameOrId("MQ_TYPES")

	record := models.NewRecord(mqTypeCollection)

	record.Set("MQ_ID", 0)
	record.Set("TYPE", "IBM")
	if err := app.Dao().SaveRecord(record); err != nil {
		return err
	}
	record = models.NewRecord(mqTypeCollection)
	record.Set("MQ_ID", 1)
	record.Set("TYPE", "RabbitMQ")
	if err := app.Dao().SaveRecord(record); err != nil {
		return err
	}
	record = models.NewRecord(mqTypeCollection)
	record.Set("MQ_ID", 2)
	record.Set("TYPE", "Kafka")
	if err := app.Dao().SaveRecord(record); err != nil {
		return err
	}

	mqsCollection := &models.Collection{
		Name:       "MQS",
		CreateRule: types.Pointer(""),
		ViewRule:   types.Pointer(""),
		ListRule:   types.Pointer(""),
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
	mqsCollection = nil
	mqsCollection, err = app.Dao().FindCollectionByNameOrId("MQS")

	if err != nil {
		return nil
	}

	// mqLinksCollection := &models.Collection{
	// 	Name:       "MQ_LINKS",
	// 	Type:       models.CollectionTypeBase,
	// 	CreateRule: types.Pointer(""),
	// 	ViewRule:   types.Pointer(""),
	// 	ListRule:   types.Pointer(""),
	// 	Schema: schema.NewSchema(
	// 		&schema.SchemaField{
	// 			Name: "source",
	// 			Type: schema.FieldTypeRelation,
	// 			Options: schema.RelationOptions{
	// 				CollectionId: mqsCollection.Id,
	// 				MaxSelect:    types.Pointer(1), // 0 indicates no limit, allowing multiple selections
	// 			},
	// 		},
	// 		&schema.SchemaField{
	// 			Name: "destination",
	// 			Type: schema.FieldTypeRelation,
	// 			Options: schema.RelationOptions{
	// 				CollectionId: mqsCollection.Id,
	// 				MaxSelect:    types.Pointer(1), // 0 indicates no limit, allowing multiple selections
	// 			},
	// 		},
	// 		&schema.SchemaField{
	// 			Name:        "connectionFriendlyName",
	// 			Type:        schema.FieldTypeText,
	// 			Presentable: true,
	// 		},
	// 		// &schema.SchemaField{
	// 		// 	Name: "FieldPath",
	// 		// 	Type: schema.FieldTypeRelation,
	// 		// 	Options: schema.RelationOptions{
	// 		// 		CollectionId: templateCollection.Id,
	// 		// 		MaxSelect:    nil,
	// 		// 	},
	// 		// },
	// 	),
	// }

	// err = app.Dao().SaveCollection(mqLinksCollection)
	// if err != nil {
	// 	return err
	// }

	// mqLinksCollection = nil
	// mqLinksCollection, err = app.Dao().FindCollectionByNameOrId("MQ_LINKS")
	// if err != nil {
	// 	return err
	// }

	mqFiltersCollection := &models.Collection{
		Name:       "MQ_FILTERS",
		ViewRule:   types.Pointer(""),
		CreateRule: types.Pointer(""),
		ListRule:   types.Pointer(""),
		Type:       models.CollectionTypeBase,
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
		),
	}

	err = app.Dao().SaveCollection(mqFiltersCollection)
	if err != nil {
		return err
	}

	return nil
}
func checkMainCollections(app *pocketbase.PocketBase) error {

	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {

		//Check MQS collection
		_, err := app.Dao().FindCollectionByNameOrId("MQS")

		if err != nil {
			_ = createMQConfigCollections(app)
		}

		return nil
	})

	return nil
}

func CreateCollection(colName string, app *pocketbase.PocketBase) error {

	collection := &models.Collection{
		Name:       colName,
		Type:       models.CollectionTypeBase,
		CreateRule: types.Pointer(""),
		ViewRule:   types.Pointer(""),
		ListRule:   types.Pointer(""),

		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name:        "FieldPath",
				Type:        schema.FieldTypeText,
				Required:    true,
				Presentable: true,
			},
			&schema.SchemaField{
				Name:     "Enabled",
				Type:     schema.FieldTypeBool,
				Required: false,
			},
		),
	}

	if err := app.Dao().SaveCollection(collection); err != nil {
		return err
	}

	return nil
}
