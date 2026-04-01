package dlq

import (
	"log"
	"os"
	"strconv"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/models"
)

// DLQEntry represents a failed message to be stored in the dead letter queue.
type DLQEntry struct {
	OriginalMessage string
	ErrorReason     string
	FilterID        string
	SourceQueue     string
	RetryCount      int
}

// MaxRetries returns the maximum number of DLQ retries (configurable via MQ_DLQ_MAX_RETRIES env var).
func MaxRetries() int {
	if v := os.Getenv("MQ_DLQ_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 3
}

// SendToDLQ stores a failed message in the DLQ collection.
func SendToDLQ(app *pocketbase.PocketBase, entry DLQEntry) error {
	collection, err := app.Dao().FindCollectionByNameOrId("DLQ")
	if err != nil {
		log.Printf("DLQ collection not found: %v", err)
		return err
	}

	record := models.NewRecord(collection)
	record.Set("original_message", entry.OriginalMessage)
	record.Set("error_reason", entry.ErrorReason)
	record.Set("filter_id", entry.FilterID)
	record.Set("source_queue", entry.SourceQueue)
	record.Set("retry_count", entry.RetryCount)

	if err := app.Dao().SaveRecord(record); err != nil {
		log.Printf("Failed to save DLQ record: %v", err)
		return err
	}

	log.Printf("Message sent to DLQ for filter %s: %s", entry.FilterID, entry.ErrorReason)
	return nil
}

// DeleteMessage removes a DLQ entry by ID.
func DeleteMessage(app *pocketbase.PocketBase, id string) error {
	record, err := app.Dao().FindRecordById("DLQ", id)
	if err != nil {
		return err
	}
	return app.Dao().DeleteRecord(record)
}

// ListMessages returns DLQ entries with pagination.
func ListMessages(app *pocketbase.PocketBase, page, perPage int) ([]*models.Record, int, error) {
	collection, err := app.Dao().FindCollectionByNameOrId("DLQ")
	if err != nil {
		return nil, 0, err
	}

	records := []*models.Record{}
	query := app.Dao().RecordQuery(collection.Name)
	if err := query.All(&records); err != nil {
		return nil, 0, err
	}

	total := len(records)

	// Manual pagination
	start := (page - 1) * perPage
	if start >= total {
		return []*models.Record{}, total, nil
	}
	end := start + perPage
	if end > total {
		end = total
	}

	return records[start:end], total, nil
}

// GetMessage retrieves a single DLQ entry by ID.
func GetMessage(app *pocketbase.PocketBase, id string) (*models.Record, error) {
	return app.Dao().FindRecordById("DLQ", id)
}

// IncrementRetryCount increments the retry count on a DLQ record.
func IncrementRetryCount(app *pocketbase.PocketBase, id string) error {
	record, err := app.Dao().FindRecordById("DLQ", id)
	if err != nil {
		return err
	}
	currentCount := record.GetInt("retry_count")
	record.Set("retry_count", currentCount+1)
	return app.Dao().SaveRecord(record)
}
