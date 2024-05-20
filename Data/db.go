package Data

import (
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

	return app
}

func CreateCollection(colName string, app *pocketbase.PocketBase) error {

	collection := &models.Collection{
		Name:       colName,
		Type:       models.CollectionTypeBase,
		CreateRule: types.Pointer(""),
		ListRule:   types.Pointer(""),

		Schema: schema.NewSchema(
			&schema.SchemaField{
				Name:     "FieldPath",
				Type:     schema.FieldTypeText,
				Required: true,
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
