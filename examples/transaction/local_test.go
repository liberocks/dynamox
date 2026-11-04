package transaction

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/examples/transaction/source"
	"github.com/liberocks/dynamox/examples/transaction/target"
	"github.com/liberocks/dynamox/runtime"
)

func TestDynamoDBLocalCrossClientTransactionAndTableOverrides(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_LOCAL_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DYNAMODB_LOCAL_ENDPOINT to run DynamoDB Local integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := dynamodb.New(dynamodb.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("local", "local", ""),
		BaseEndpoint: aws.String(endpoint),
	})
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceTable, targetTable := "dynamox_source_it_"+suffix, "dynamox_target_it_"+suffix
	for _, name := range []string{sourceTable, targetTable} {
		_, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName: aws.String(name),
			AttributeDefinitions: []types.AttributeDefinition{
				{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS},
				{AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS},
			},
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange},
			},
			BillingMode: types.BillingModePayPerRequest,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)})
		})
		if err := dynamodb.NewTableExistsWaiter(db).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}

	sourceClient := source.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"source-records": sourceTable}})
	targetClient := target.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"target-records": targetTable}})
	const pk = "ENTITY#one"
	sourceKey := source.SourceRecordKey{PK: pk, SK: "SOURCE#one"}
	seed := source.SourceRecord{PK: sourceKey.PK, SK: sourceKey.SK, Status: "pending", UpdatedAt: time.Now().UTC()}
	if _, err := sourceClient.CreateSourceRecord(ctx, seed); err != nil {
		t.Fatalf("seed source record through its table override: %v", err)
	}
	targetRecord := target.TargetRecord{PK: pk, SK: "TARGET#one", Status: "processed", Details: []target.Detail{}, Payload: runtime.JSON(map[string]any{"source": "integration"})}
	if err := CommitRecords(ctx, sourceClient, targetClient, sourceKey, targetRecord); err != nil {
		t.Fatal(err)
	}
	updated, found, err := sourceClient.GetSourceRecordWithOptions(ctx, sourceKey, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || updated.Status != "processed" {
		t.Fatalf("source update missing after transaction: record=%#v found=%v err=%v", updated, found, err)
	}
	created, found, err := targetClient.GetTargetRecordWithOptions(ctx, target.TargetRecordKey{PK: targetRecord.PK, SK: targetRecord.SK}, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || created.Status != "processed" {
		t.Fatalf("target record missing after transaction: record=%#v found=%v err=%v", created, found, err)
	}

	// A source record outside the expected state must cancel both writes.
	blockedKey := source.SourceRecordKey{PK: pk, SK: "SOURCE#blocked"}
	if _, err := sourceClient.CreateSourceRecord(ctx, source.SourceRecord{PK: blockedKey.PK, SK: blockedKey.SK, Status: "blocked"}); err != nil {
		t.Fatal(err)
	}
	targetRecord.SK = "TARGET#blocked"
	err = CommitRecords(ctx, sourceClient, targetClient, blockedKey, targetRecord)
	var cancelled *types.TransactionCanceledException
	conditionalFailure := false
	if errors.As(err, &cancelled) {
		for _, reason := range cancelled.CancellationReasons {
			conditionalFailure = conditionalFailure || aws.ToString(reason.Code) == "ConditionalCheckFailed"
		}
	}
	if !conditionalFailure {
		t.Fatalf("expected conditional transaction cancellation, got %v", err)
	}
	unchanged, found, err := sourceClient.GetSourceRecordWithOptions(ctx, blockedKey, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || unchanged.Status != "blocked" {
		t.Fatalf("failed transaction changed its source record: record=%#v found=%v err=%v", unchanged, found, err)
	}
	_, found, err = targetClient.GetTargetRecordWithOptions(ctx, target.TargetRecordKey{PK: targetRecord.PK, SK: targetRecord.SK}, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || found {
		t.Fatalf("failed transaction created a target record: found=%v err=%v", found, err)
	}
}
