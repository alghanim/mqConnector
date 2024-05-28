package routes

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mqConnector/Data"
	tools "mqConnector/Tools"
	"mqConnector/models"
	"mqConnector/mq"
	"net/http"
	"strings"

	"github.com/clbanning/mxj/v2"
	pbmodels "github.com/pocketbase/pocketbase/models"

	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

func InitRoutes(app *pocketbase.PocketBase) {

	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		// Check if the collection exists

		collections, err := app.Dao().FindCollectionByNameOrId("MQ_LINKS")
		if err == nil {
			if collections.Name == "MQ_LINKS" {

				// var source models.MQConfig
				// var destination []models.MQConfig
				// var mqCon map[string]interface{}
				query := app.Dao().RecordQuery(collections.Name)
				if err != nil {
					return err
				}

				records := []*pbmodels.Record{}
				if err := query.All(&records); err != nil {
					return err
				}

				for _, record := range records {
					if errs := app.Dao().ExpandRecord(record, []string{"source", "destination"}, nil); len(errs) > 0 {
						return fmt.Errorf("failed to expand: %v", errs)
					}

					expandedRecord := record.Expand()

					sourceRecord, ok := expandedRecord["source"].(*pbmodels.Record)
					if !ok {
						return err
					}

					sourceConfig := map[string]string{

						"queueManager": sourceRecord.GetString("queueManager"),
						"connName":     sourceRecord.GetString("connName"),
						"channel":      sourceRecord.GetString("channel"),
						"user":         sourceRecord.GetString("user"),
						"password":     sourceRecord.GetString("password"),
						"queueName":    sourceRecord.GetString("queueName"),
						"url":          sourceRecord.GetString("url"),
						"brokers":      sourceRecord.GetString("brokers"),
						"topic":        sourceRecord.GetString("topic"),
					}

					// connectionType := sourceRecord["type"]
					if errs := app.Dao().ExpandRecord(sourceRecord, []string{"type"}, nil); len(errs) > 0 {
						return err
					}

					sourceConType := sourceRecord.Expand()
					SourceConnnectionType, ok := sourceConType["type"].(*pbmodels.Record)
					if !ok {
						return err
					}

					sourceConfig["type"] = SourceConnnectionType.GetString("TYPE")

					destinationRecord, ok := expandedRecord["destination"].(*pbmodels.Record)
					if !ok {
						return err
					}

					destinationConfig := map[string]string{
						"queueManager": destinationRecord.GetString("queueManager"),
						"connName":     destinationRecord.GetString("connName"),
						"channel":      destinationRecord.GetString("channel"),
						"user":         destinationRecord.GetString("user"),
						"password":     destinationRecord.GetString("password"),
						"queueName":    destinationRecord.GetString("queueName"),
						"url":          destinationRecord.GetString("url"),
						"brokers":      destinationRecord.GetString("brokers"),
						"topic":        destinationRecord.GetString("topic"),
					}
					// connectionFriendlyName := sourceRecord.GetString("user")

					// fieldPaths := expandedRecord["FieldPath"].([]*pbmodels.Record)

					if errs := app.Dao().ExpandRecord(destinationRecord, []string{"type"}, nil); len(errs) > 0 {
						return err
					}

					destinationConType := sourceRecord.Expand()
					destinationConnnectionType, ok := destinationConType["type"].(*pbmodels.Record)
					if !ok {
						return err
					}

					destinationConfig["type"] = destinationConnnectionType.GetString("TYPE")
					log.Println(sourceConfig, destinationConfig)
					var sourceMQConnector mq.MQConnector
					sourceMQConnector, err = mq.NewMQConnector(mq.GetQueueType(sourceConfig["type"]), sourceConfig)
					if err != nil {
						log.Fatalf("Failed to create MQ connector: %v", err)
					}
					// Connect to MQ

					var destinationMQConnector mq.MQConnector
					destinationMQConnector, err = mq.NewMQConnector(mq.GetQueueType(destinationConfig["type"]), destinationConfig)
					if err != nil {
						log.Fatalf("Failed to create MQ connector: %v", err)
					}

					go func() {

						err = sourceMQConnector.Connect()
						if err != nil {
							log.Fatalf("Failed to connect to MQ: %v", err)
						}
						defer sourceMQConnector.Disconnect()
						for {
							msg, err := sourceMQConnector.ReceiveMessage()
							if err != nil {
								log.Fatalf("Failed to receive message: %v", err)
							}
							fmt.Printf("Received message: %s\n", string(msg))

							err = destinationMQConnector.Connect()
							if err != nil {
								log.Fatalf("Failed to connect to MQ: %v", err)
							}

							defer destinationMQConnector.Disconnect()

							if msg != nil {
								err = destinationMQConnector.SendMessage(msg)
								if err != nil {
									log.Fatalf("Failed to send message: %v", err)
								}
							}

						}
					}()

				}

			}

		} else {
			return err
		}

		return nil
	})
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		collections, err := app.Dao().FindCollectionsByType(pbmodels.CollectionTypeBase)
		if err != nil {
			return err
		}
		// Initialize the map
		collectionsMap := make(map[string][]models.ConfigEntry)
		for _, col := range collections {

			var configPaths []models.ConfigEntry

			if col.Name != "mq_config" && !strings.Contains(col.Name, "MQ") && !strings.Contains(col.Name, "config") {

				fmt.Println(col.Name)
				err := app.DB().Select("FieldPath").From(col.Name).All(&configPaths)
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
				// fname := c.FormValue("fname")

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

				// if rootTag != "" {
				// 	err := Data.CreateCollection(rootTag, app)
				// 	if err != nil {
				// 		return err
				// 	}
				// } else {
				// 	return fmt.Errorf("collection exists")
				// }

				collection, err := app.Dao().FindCollectionByNameOrId("Templates")
				if err != nil {
					return err
				}
				paths := tools.ExtractPaths(fileBytes)

				for _, config := range paths {
					record := pbmodels.NewRecord(collection)

					record.Set("FieldPath", rootTag+"."+config.FieldPath)
					// record.Set("Enabled", config.Enabled)
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
				apis.ActivityLogger(app),
			},
		},
		)
		return nil
	})

	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {

		e.Router.AddRoute(echo.Route{
			Method: http.MethodGet,
			Path:   "/api/connections/:connection",
			Handler: func(c echo.Context) error {

				conn := c.PathParam("connection")
				record, err := app.Dao().FindFirstRecordByData("MQ_FILTERS", "name", conn)
				if err != nil {

					return err
				}

				if errs := app.Dao().ExpandRecord(record, []string{"FieldPath", "connection"}, nil); len(errs) > 0 {
					return fmt.Errorf("failed to expand: %v", errs)
				}

				expandedRecord := record.Expand()

				connectionRecord, ok := expandedRecord["connection"].(*pbmodels.Record)
				if !ok {
					return c.JSON(http.StatusInternalServerError, map[string]string{
						"error": "failed to convert connection record",
					})
				}
				connectionFriendlyName := connectionRecord.GetString("connectionFriendlyName")

				fieldPaths := expandedRecord["FieldPath"].([]*pbmodels.Record)

				// Construct the custom response
				customResponse := map[string]interface{}{
					"Connection": map[string]interface{}{
						"connectionFriendlyName": connectionFriendlyName,
					},
					"FieldPaths": []map[string]interface{}{},
				}

				for _, fieldPath := range fieldPaths {
					fp := fieldPath
					customResponse["FieldPaths"] = append(customResponse["FieldPaths"].([]map[string]interface{}), map[string]interface{}{
						"FieldPath": fp.GetString("FieldPath"),
						"T_NAME":    fp.GetString("T_NAME"),
					})
				}

				return c.JSON(http.StatusOK, customResponse)
			},
			Middlewares: []echo.MiddlewareFunc{
				apis.ActivityLogger(app),
			},
		},
		)
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
	// app.OnRecordAfterUpdateRequest().Add(func(e *core.RecordUpdateEvent) error {
	// 	col := e.Record.Collection().Name
	// 	configs := []models.ConfigEntry{}
	// 	query := fmt.Sprintf("SELECT FieldPath,Enabled FROM %s where Enabled = False", col)
	// 	err := app.Dao().DB().
	// 		NewQuery(query).
	// 		All(&configs)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	Data.UpdateCollection(col, configs)

	// 	return nil
	// })

}
