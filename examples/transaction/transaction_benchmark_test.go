package transaction

import (
	"context"
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
	"github.com/liberocks/dynamox/provision"
	"github.com/liberocks/dynamox/runtime"
)

func BenchmarkDynamoxCrossClientConditionalTransaction(b *testing.B) {
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		b.Skip("set DYNAMODB_ENDPOINT to run DynamoDB emulator benchmarks")
	}
	ctx := context.Background()
	db := dynamodb.New(dynamodb.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("local", "local", ""),
		BaseEndpoint: aws.String(endpoint),
	})
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceTable, targetTable := "dynamox_bench_source_"+suffix, "dynamox_bench_target_"+suffix
	created, err := provision.Sync(ctx, db, []provision.Table{
		{
			Name:      sourceTable,
			Partition: provision.Key{Name: "PK", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash},
			Sort:      provision.Key{Name: "SK", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeRange},
		},
		{
			Name:      targetTable,
			Partition: provision.Key{Name: "PK", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash},
			Sort:      provision.Key{Name: "SK", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeRange},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(created[i])}); err != nil {
				b.Logf("delete benchmark table %s: %v", created[i], err)
			}
		}
	})
	sourceClient := source.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"source-records": sourceTable}})
	targetClient := target.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"target-records": targetTable}})
	pendingKey := source.SourceRecordKey{PK: "ENTITY#benchmark", SK: "SOURCE#record"}
	pending := "pending"
	if _, err := sourceClient.CreateSourceRecord(ctx, source.SourceRecord{PK: pendingKey.PK, SK: pendingKey.SK, Status: pending}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i > 0 {
			b.StopTimer()
			if err := sourceClient.UpdateSourceRecord(ctx, pendingKey, source.SourceRecordUpdate{SetStatus: &pending}); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
		}
		record := target.TargetRecord{
			PK:              pendingKey.PK,
			SK:              fmt.Sprintf("TARGET#%d", i),
			Status:          "processed",
			Details:        []target.Detail{},
			ProviderPayload: runtime.JSON(map[string]any{"source": "benchmark"}),
		}
		if err := CommitRecords(ctx, sourceClient, targetClient, pendingKey, record); err != nil {
			b.Fatal(err)
		}
	}
}
