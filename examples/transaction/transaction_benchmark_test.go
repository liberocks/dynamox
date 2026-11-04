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
	"github.com/liberocks/dynamox/examples/transaction/admin"
	"github.com/liberocks/dynamox/examples/transaction/learner"
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
	adminTable, learnerTable := "dynamox_bench_admin_"+suffix, "dynamox_bench_learner_"+suffix
	created, err := provision.Sync(ctx, db, []provision.Table{
		{
			Name:      adminTable,
			Partition: provision.Key{Name: "PK", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash},
			Sort:      provision.Key{Name: "SK", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeRange},
		},
		{
			Name:      learnerTable,
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
	adminClient := admin.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"admin-drafts": adminTable}})
	learnerClient := learner.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"learner-content": learnerTable}})
	draftKey := admin.DraftKey{PK: "EXAM#benchmark", SK: "DRAFT#publish"}
	unpublished := "unpublished"
	if _, err := adminClient.CreateDraft(ctx, admin.Draft{PK: draftKey.PK, SK: draftKey.SK, Status: unpublished}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i > 0 {
			b.StopTimer()
			if err := adminClient.UpdateDraft(ctx, draftKey, admin.DraftUpdate{SetStatus: &unpublished}); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
		}
		content := learner.Content{
			PK:              draftKey.PK,
			SK:              fmt.Sprintf("QUIZ#%d", i),
			Status:          "published",
			Sections:        []learner.Section{},
			ProviderPayload: runtime.JSON(map[string]any{"source": "benchmark"}),
		}
		if err := Publish(ctx, adminClient, learnerClient, draftKey, content); err != nil {
			b.Fatal(err)
		}
	}
}
