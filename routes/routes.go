package routes

import (
	"bytes"
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

	"github.com/beevik/etree"
	"github.com/clbanning/mxj/v2"
	pbmodels "github.com/pocketbase/pocketbase/models"

	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

var dataMap = make(map[string][]map[string]map[string]string)

func InitRoutes(app *pocketbase.PocketBase) {

	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		// Check if the collection exists
		filterCollection, err := app.Dao().FindCollectionByNameOrId("MQ_FILTERS")
		if err != nil {
			return err
		}

		filtersQuery := app.Dao().RecordQuery(filterCollection.Name)
		if err != nil {
			return err
		}

		filterRecords := []*pbmodels.Record{}
		if err := filtersQuery.All(&filterRecords); err != nil {
			return err
		}

		app.Dao().ExpandRecords(filterRecords, []string{"FieldPath", "source", "destination"}, nil)

		for _, filterRecord := range filterRecords {

			expandedRecord := filterRecord.Expand()
			source, ok := expandedRecord["source"].(*pbmodels.Record)
			if !ok {
				return err
			}
			destination, ok := expandedRecord["destination"].(*pbmodels.Record)
			if !ok {
				return err
			}

			paths, ok := expandedRecord["FieldPath"].([]*pbmodels.Record)
			if !ok {
				log.Println("Paths are not loaded or empty")
			}
			// log.Println("check this", testrecord)

			go func(paths []*pbmodels.Record, source *pbmodels.Record, destination *pbmodels.Record) error {

				var filterPaths []string
				for _, path := range paths {

					filterPaths = append(filterPaths, path.GetString("FieldPath"))
					fmt.Println(path.GetString("FieldPath"))
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
				SourceConnnectionType, ok := sourceType["type"].(*pbmodels.Record)
				if !ok {
					return fmt.Errorf("source connection has not been loaded , due to a problem with the type: %s", sourceType["type"])
				}

				sourceConn["type"] = SourceConnnectionType.GetString("TYPE")

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
				destinationConnnectionType, ok := destType["type"].(*pbmodels.Record)
				if !ok {
					return fmt.Errorf("destination connection has not been loaded , due to a problem with the type: %s", destType["type"])
				}

				destConn["type"] = destinationConnnectionType.GetString("TYPE")

				var sourceMQConnector mq.MQConnector
				sourceMQConnector, err = mq.NewMQConnector(mq.GetQueueType(sourceConn["type"]), sourceConn)
				if err != nil {
					return fmt.Errorf("Failed to create MQ source connector: %v", err)
				}

				var destinationMQConnector mq.MQConnector
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
					msg, err := sourceMQConnector.ReceiveMessage()
					if err != nil {
						return fmt.Errorf("Failed to receive message: %v", err)
					}

					format, err := tools.DetectFormat(msg)
					if err != nil {
						log.Println("unknown format")
						continue
					}

					var msgData []byte
					// var processedData map[string]interface{}
					var response []byte

					if format == "XML" {
						// do XML shit
						// Parse the XML document
						doc := etree.NewDocument()
						if err := doc.ReadFromString(string(msg)); err != nil {
							log.Printf("Error in reading the XML message: %v", err)
							continue
						}
						namespace := doc.Root().Space

						for _, p := range filterPaths {
							tools.RemoveElements(doc.Root(), namespace, p)
							// Find the <cont:item> element
							itemsElement := tools.FindElement(doc.Root(), namespace, p)
							if itemsElement != nil {
								// Find the <cont:item> element within <cont:items>
								itemElement := tools.FindElement(itemsElement, namespace, p)
								if itemElement != nil {
									fmt.Printf("Found item: %s\n", itemElement.Text())
								} else {
									fmt.Println("Item element not found")
								}
							} else {
								fmt.Println("Items element not found")
							}
						}

						// Serialize the modified XML document
						result, err := doc.WriteToString()
						if err != nil {
							panic(err)
						}

						// mv, err := mxj.NewMapXml(msg)
						// if err != nil {
						// 	return
						// }
						// msgData, err = json.Marshal(mv)
						// if err != nil {
						// 	return
						// }
						// filteredJson, err := tools.RemoveJSONPaths(msgData, filterPaths)

						// if err != nil {
						// 	return
						// }
						// if err := json.Unmarshal(filteredJson, &processedData); err != nil {
						// 	// return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert JSON to map"})
						// }

						// // Convert map back to XML
						// response, err = mxj.Map(processedData).Xml()
						// if err != nil {
						// 	// return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert map to XML"})
						// }
						response = []byte(result)
					} else if format == "JSON" {
						//do JSON shit
						msgData = msg
						filteredJson, err := tools.RemoveJSONPaths(msgData, filterPaths)

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

			}(paths, source, destination)

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
				var processedData map[ // Convert JSON back to map
				string]interface{}
				if err := json.Unmarshal(filteredJson, &processedData); err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert JSON to map"})
				}
				xmlResponse, err := mxj.Map(processedData).Xml()
				if err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to convert map to XML"})
				}
				return c.XMLBlob(http.StatusOK, xmlResponse)
			},
			Middlewares: []echo.MiddlewareFunc{},
			Name:        "",
		})
		return nil
	})
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {

		e.Router.AddRoute(echo.Route{
			Method: http.MethodPost,
			Path:   "/api/v2/upload/sample",
			Handler: func(c echo.Context) error {
				// Retrieve the file from form data
				file, err := c.FormFile("file")
				if err != nil {
					return c.String(http.StatusBadRequest, "Failed to read file from request")
				}

				// Open the uploaded file
				src, err := file.Open()
				if err != nil {
					return err
				}
				defer src.Close()

				// Read the file content into a buffer
				buf := new(bytes.Buffer)
				buf.ReadFrom(src)

				// Parse the XML content using etree
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

				log.Println(namespace)
				// Extract all tags without duplicates
				tagSet := make(map[string]struct{})
				tools.ExtractTags(doc.Root(), tagSet)

				// Convert the set of tags to a slice
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

				// Return the extracted tags as a JSON response
				return c.JSON(http.StatusOK, tags)
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

				format, err := tools.DetectFormat(fileBytes)
				if err != nil {
					return c.String(http.StatusBadRequest, "unknown format")
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
