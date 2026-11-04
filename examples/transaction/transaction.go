// Package transaction demonstrates a conditional write assembled from
// operations created by two independently generated clients.
package transaction

import (
	"context"

	"github.com/liberocks/dynamox/examples/transaction/source"
	"github.com/liberocks/dynamox/examples/transaction/target"
	"github.com/liberocks/dynamox/runtime"
)

func CommitRecords(ctx context.Context, sourceClient *source.Client, targetClient *target.Client, sourceKey source.SourceRecordKey, targetRecord target.TargetRecord) error {
	processed := "processed"
	sourceUpdate, err := sourceClient.UpdateSourceRecordOperationWithOptions(
		sourceKey,
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
	targetCreate, err := targetClient.CreateTargetRecordOperation(targetRecord)
	if err != nil {
		return err
	}
	return sourceClient.TransactWrite(ctx, sourceUpdate, targetCreate)
}
