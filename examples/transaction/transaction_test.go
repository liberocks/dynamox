package transaction

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/examples/transaction/source"
	"github.com/liberocks/dynamox/examples/transaction/target"
	"github.com/liberocks/dynamox/runtime"
)

type transactionFailureDB struct {
	runtime.DynamoAPI
	err error
}

func (d transactionFailureDB) TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	return nil, d.err
}

func TestCommitRecordsReturnsTransactionResult(t *testing.T) {
	ctx := context.Background()
	sourceClient := source.NewClient(transactionFailureDB{})
	targetClient := target.NewClient(transactionFailureDB{})
	key := source.SourceRecordKey{PK: "ENTITY#one", SK: "SOURCE#one"}
	record := target.TargetRecord{PK: key.PK, SK: "TARGET#one", Status: "processed", Details: []target.Detail{}, Payload: runtime.JSON(map[string]any{})}
	if err := CommitRecords(ctx, sourceClient, targetClient, key, record); err != nil {
		t.Fatalf("successful transaction returned an error: %v", err)
	}
	want := &types.TransactionCanceledException{}
	sourceClient = source.NewClient(transactionFailureDB{err: want})
	targetClient = target.NewClient(transactionFailureDB{err: want})
	err := CommitRecords(ctx, sourceClient, targetClient, key, record)
	var got *types.TransactionCanceledException
	if !errors.As(err, &got) || got != want {
		t.Fatalf("transaction cancellation type was not preserved: %v", err)
	}
}

func TestSeparateSchemasKeepOverridesAndBuildMixedOperations(t *testing.T) {
	db := runtime.DynamoAPI(nil)
	sourceClient := source.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"source-records": "source-prod"}})
	targetClient := target.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"target-records": "target-prod"}})
	status := "processed"
	update, err := sourceClient.UpdateSourceRecordOperationWithOptions(source.SourceRecordKey{PK: "ENTITY#one", SK: "SOURCE#one"}, source.SourceRecordUpdate{SetStatus: &status}, runtime.WriteOptions{Condition: &runtime.Expression{Text: "#status = :pending", Names: map[string]string{"#status": "status"}, Values: map[string]any{":pending": "pending"}}})
	if err != nil {
		t.Fatal(err)
	}
	put, err := targetClient.CreateTargetRecordOperation(target.TargetRecord{PK: "ENTITY#one", SK: "TARGET#one", Status: "processed"})
	if err != nil {
		t.Fatal(err)
	}
	if update.Table != "source-prod" || put.Table != "target-prod" {
		t.Fatalf("physical table overrides were lost: %q %q", update.Table, put.Table)
	}
	if update.Condition == nil || !put.CreateOnly {
		t.Fatalf("expected a conditional update and create-only operation: %#v %#v", update, put)
	}
}
