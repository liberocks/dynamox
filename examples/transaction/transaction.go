// Package transaction shows a conditional conditional transaction assembled from
// independently generated source and target clients.
package transaction

import (
	"context"

	"github.com/liberocks/dynamox/examples/transaction/source"
	"github.com/liberocks/dynamox/examples/transaction/target"
	"github.com/liberocks/dynamox/runtime"
)

func CommitRecords(ctx context.Context, sourceClient *source.Client, targetClient *target.Client, pendingKey source.SourceRecordKey, record target.TargetRecord) error {
	processed := "processed"
	pendingUpdate, err := sourceClient.UpdateSourceRecordOperationWithOptions(
		pendingKey,
		source.SourceRecordUpdate{SetStatus: &processed},
		runtime.WriteOptions{Condition: &runtime.Expression{
			Text:   "#status = :pending",
			Names:  map[string]string{"#status": "status"},
			Values: map[string]any{":pending": "pending"},
		}},
	)
	if err != nil {
		return err
	}
	recordCreate, err := targetClient.CreateTargetRecordOperation(record)
	if err != nil {
		return err
	}
	return sourceClient.TransactWrite(ctx, pendingUpdate, recordCreate)
}
