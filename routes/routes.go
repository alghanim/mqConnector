package routes

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mqConnector/Data"
	tools "mqConnector/Tools"
	"mqConnector/models"
	"net/http"
	"strings"

	"github.com/clbanning/mxj/v2"
	"github.com/pocketbase/dbx"
	pbmodels "github.com/pocketbase/pocketbase/models"

	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

func InitRoutes(app *pocketbase.PocketBase) {

	// app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
	// 	// Check if the collection exists

	// 	collections, err := app.Dao().FindCollectionByNameOrId("mq_config")
	// 	if err == nil {
	// 		if collections.Name == "mq_config" {

	// 			var mqConfigs []models.MQConfig
	// 			err = app.DB().Select("*").From(collections.Name).All(&mqConfigs)
	// 			if err != nil {
	// 				return err
	// 			}

	// 			for _, mqConfig := range mqConfigs {

	// 				config := map[string]string{
	// 					"queueManager": mqConfig.QueueManager,
	// 					"connName":     mqConfig.ConnName,
	// 					"channel":      mqConfig.Channel,
	// 					"user":         mqConfig.User,
	// 					"password":     mqConfig.Password,
	// 					"queueName":    mqConfig.QueueName,
	// 					"url":          mqConfig.URL,
	// 					"brokers":      mqConfig.Brokers,
	// 					"topic":        mqConfig.Topic,
	// 				}

	// 				var mqConnector mq.MQConnector
	// 				mqConnector, err = mq.NewMQConnector(mq.GetQueueType(mqConfig.Type), config)
	// 				if err != nil {
	// 					log.Fatalf("Failed to create MQ connector: %v", err)
	// 				}
	// 				// Connect to MQ

	// 				go func() {

	// 					err = mqConnector.Connect()
	// 					if err != nil {
	// 						log.Fatalf("Failed to connect to MQ: %v", err)
	// 					}
	// 					defer mqConnector.Disconnect()
	// 					for {
	// 						msg, err := mqConnector.ReceiveMessage()
	// 						if err != nil {
	// 							log.Fatalf("Failed to receive message: %v", err)
	// 						}
	// 						fmt.Printf("Received message: %s\n", string(msg))
	// 					}
	// 				}()

	// 			}
	// 			return nil // Collection already exists
	// 		}
	// 		return nil
	// 	} else {
	// 		// Create the collection if it doesn't exist
	// 		collection := &pbmodels.Collection{
	// 			Name: "mq_config",
	// 			Schema: schema.NewSchema(
	// 				&schema.SchemaField{
	// 					Name: "type",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "queueManager",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "connName",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "channel",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "user",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "password",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "queueName",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "url",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "brokers",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "topic",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "source",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 				&schema.SchemaField{
	// 					Name: "destination",
	// 					Type: schema.FieldTypeText,
	// 				},
	// 			),
	// 		}

	// 		err = app.Dao().SaveCollection(collection)
	// 		if err != nil {
	// 			return err
	// 		}

	// 	}

	// 	return nil
	// })
	app.OnAfterBootstrap().Add(func(e *core.BootstrapEvent) error {
		collections, err := app.Dao().FindCollectionsByType(pbmodels.CollectionTypeBase)
		if err != nil {
			return err
		}
		// Initialize the map
		collectionsMap := make(map[string][]models.ConfigEntry)
		for _, col := range collections {

			var configPaths []models.ConfigEntry

			if col.Name != "mq_config" || !strings.Contains(col.Name, "MQ") || !strings.Contains(col.Name, "config") {

				fmt.Println(col.Name)
				err := app.DB().Select("FieldPath", "Enabled").From(col.Name).Where(dbx.NewExp("Enabled = False")).All(&configPaths)
				if err != nil {
					// return err
					continue
				}
				collectionsMap[col.Name] = configPaths
			}

		}
		Data.InitCollections(collectionsMap)

		return nil
	})

	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {

		e.Router.AddRoute(echo.Route{
			Method: http.MethodPost,
			Path:   "/api/filter",
			Handler: func(c echo.Context) error {

				file, err := c.FormFile("file")
				if err != nil {
					return c.JSON(http.StatusBadRequest, map[string]string{"error": "Failed to get file"})
				}

				// Open the file
				src, err := file.Open()
				if err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to open file"})
				}
				defer src.Close()

				// Read the file content
				xmlData, err := io.ReadAll(src)
				if err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to read file"})
				}

				// Convert XML to map
				mv, err := mxj.NewMapXml(xmlData)
				if err != nil {
					return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid XML"})
				}

				// Convert map to JSON
				jsonData, err := json.Marshal(mv)
				if err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert to JSON"})
				}

				// Process the JSON data (this is where you can modify the JSON as needed)

				rootTag, err := tools.GetRootTag(xmlData)
				if err != nil {

					return c.String(http.StatusInternalServerError, err.Error())
				}
				paths, err := Data.GetCollectionByName(rootTag)
				if err != nil {
					c.String(http.StatusInternalServerError, err.Error())
				}

				var arrayPaths []string
				for _, p := range paths {
					arrayPaths = append(arrayPaths, rootTag+"."+p.FieldPath)
				}
				log.Println(arrayPaths)
				filteredJson, err := tools.RemoveJSONPaths(jsonData, arrayPaths)

				if err != nil {
					return c.String(http.StatusInternalServerError, err.Error())
				}

				// Convert JSON back to map
				var processedData map[string]interface{}
				if err := json.Unmarshal(filteredJson, &processedData); err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert JSON to map"})
				}

				// Convert map back to XML
				xmlResponse, err := mxj.Map(processedData).Xml()
				if err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert map to XML"})
				}

				// Send the modified XML back to the client
				return c.XMLBlob(http.StatusOK, xmlResponse)

				// return c.JSON(http.StatusOK, interface{})
			},
		})
		return nil
	})
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {

		e.Router.AddRoute(echo.Route{
			Method: http.MethodPost,
			Path:   "/api/upload/sample",
			Handler: func(c echo.Context) error {

				file, err := c.FormFile("file")
				if err != nil {
					return err
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

				rootTag, err := tools.GetRootTag(fileBytes)
				if err != nil {
					return err
				}

				if rootTag != "" {
					err := Data.CreateCollection(rootTag, app)
					if err != nil {
						return err
					}
				} else {
					return fmt.Errorf("collection exists")
				}

				collection, err := app.Dao().FindCollectionByNameOrId(rootTag)
				if err != nil {
					return err
				}
				paths := tools.ExtractPaths(fileBytes)

				for _, config := range paths {
					record := pbmodels.NewRecord(collection)

					record.Set("FieldPath", config.FieldPath)
					record.Set("Enabled", config.Enabled)
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
				apis.ActivityLogger(app),
			},
		},
		)
		return nil
	})

	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		return nil
	})

	app.OnCollectionAfterDeleteRequest().Add(func(e *core.CollectionDeleteEvent) error {

		err := Data.DeleteCollection(e.Collection.Name)
		if err != nil {
			return err
		}

		return nil
	})

	// app.OnCollectionAfterCreateRequest().Add(func(e *core.CollectionCreateEvent) error {

	// 	err := Data.AddCollection(app, e.Collection.Name)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	updatedCollections := Data.GetCollections()
	// 	for _, configEntries := range updatedCollections {
	// 		jsonData, err := json.MarshalIndent(configEntries, "", "  ")
	// 		if err != nil {
	// 			log.Fatalf("failed to marshal json: %v", err)
	// 		}
	// 		fmt.Println("after create:", string(jsonData))
	// 	}
	// 	return nil
	// })
	app.OnRecordAfterUpdateRequest().Add(func(e *core.RecordUpdateEvent) error {
		col := e.Record.Collection().Name
		configs := []models.ConfigEntry{}
		query := fmt.Sprintf("SELECT FieldPath,Enabled FROM %s where Enabled = False", col)
		err := app.Dao().DB().
			NewQuery(query).
			All(&configs)
		if err != nil {
			return err
		}
		Data.UpdateCollection(col, configs)

		return nil
	})

}
