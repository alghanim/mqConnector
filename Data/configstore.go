package Data

import (
	"errors"
	"mqConnector/models"
	"sync"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
)

// Define a map to hold the collections
var collections map[string][]models.ConfigEntry
var mu sync.RWMutex

// Initialize the map (called once at the start of the application)
func InitCollections(initialData map[string][]models.ConfigEntry) {
	mu.Lock()
	defer mu.Unlock()
	collections = initialData
}

// GetCollections returns a copy of the collections map
func GetCollections() map[string][]models.ConfigEntry {
	mu.RLock()
	defer mu.RUnlock()
	copy := make(map[string][]models.ConfigEntry)
	for k, v := range collections {
		copy[k] = v
	}
	return copy
}

// GetCollectionByName returns the collection with the given name
func GetCollectionByName(collectionName string) ([]models.ConfigEntry, error) {
	mu.RLock()
	defer mu.RUnlock()
	if entries, exists := collections[collectionName]; exists {
		return entries, nil
	}
	return nil, errors.New("collection not found")
}

// DeleteCollection deletes a specific collection from the map
func DeleteCollection(collectionName string) error {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := collections[collectionName]; exists {
		delete(collections, collectionName)
		return nil
	}
	return errors.New("collection not found")
}

// UpdateCollection updates a specific collection in the map
func UpdateCollection(collectionName string, newEntries []models.ConfigEntry) {
	mu.Lock()
	defer mu.Unlock()
	collections[collectionName] = newEntries
}

// AddCollection adds a new collection to the map by querying the database
func AddCollection(app *pocketbase.PocketBase, collectionName string) error {
	mu.Lock()
	defer mu.Unlock()

	// Query the database for the new collection's entries
	var configPaths []models.ConfigEntry
	err := app.DB().Select("FieldPath").From("Templates").Where(dbx.NewExp("T_NAME = {:tname}", dbx.Params{"tname": collectionName})).All(&configPaths)
	if err != nil {
		return err
	}

	// Add the new collection to the map
	collections[collectionName] = configPaths
	return nil
}
