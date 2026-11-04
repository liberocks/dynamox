package runtime_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/provision"
	dynamoxruntime "github.com/liberocks/dynamox/runtime"
)

type benchmarkItem struct {
	ID     string `dynamodbav:"id"`
	Tenant string `dynamodbav:"tenant"`
	Value  string `dynamodbav:"value"`
}

type benchmarkKey struct {
	ID string
}

func newBenchmarkTable(b *testing.B) (context.Context, dynamoxruntime.Table[benchmarkItem, benchmarkKey]) {
	b.Helper()
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
	tableName := fmt.Sprintf("dynamox_bench_%d", time.Now().UnixNano())
	created, err := provision.Sync(ctx, db, []provision.Table{{
		Name:      tableName,
		Partition: provision.Key{Name: "id", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash},
		Indexes: []provision.Index{{
			Name:      "ByTenant",
			Partition: provision.Key{Name: "tenant", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash},
		}},
	}})
	if err != nil {
		b.Fatal(err)
	}
	if len(created) != 1 {
		b.Fatalf("expected a fresh benchmark table, created %v", created)
	}
	b.Cleanup(func() {
		if _, err := db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(tableName)}); err != nil {
			b.Logf("delete benchmark table %s: %v", tableName, err)
		}
	})
	table := dynamoxruntime.Table[benchmarkItem, benchmarkKey]{
		DB:            db,
		Name:          tableName,
		PartitionName: "id",
		Codec: dynamoxruntime.Codec[benchmarkItem]{
			Encode: func(item benchmarkItem) (map[string]types.AttributeValue, error) {
				return attributevalue.MarshalMap(item)
			},
			Decode: func(item map[string]types.AttributeValue) (benchmarkItem, error) {
				var result benchmarkItem
				err := attributevalue.UnmarshalMap(item, &result)
				return result, err
			},
		},
		Key: func(key benchmarkKey) (map[string]types.AttributeValue, error) {
			value, err := attributevalue.Marshal(key.ID)
			if err != nil {
				return nil, err
			}
			return map[string]types.AttributeValue{"id": value}, nil
		},
	}
	if err := table.Create(ctx, benchmarkItem{ID: "seed", Tenant: "tenant-1", Value: "initial"}); err != nil {
		b.Fatal(err)
	}
	return ctx, table
}

func BenchmarkDynamoxRuntime(b *testing.B) {
	b.Run("Get", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item, found, err := table.Get(ctx, benchmarkKey{ID: "seed"})
			if err != nil || !found || item.Value != "initial" {
				b.Fatalf("Get result: found=%t item=%#v err=%v", found, item, err)
			}
		}
	})

	b.Run("GetConsistentRead", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		options := dynamoxruntime.ReadOptions{ConsistentRead: true}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item, found, err := table.GetWithOptions(ctx, benchmarkKey{ID: "seed"}, options)
			if err != nil || !found || item.Value != "initial" {
				b.Fatalf("consistent Get result: found=%t item=%#v err=%v", found, item, err)
			}
		}
	})

	b.Run("Create", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item := benchmarkItem{ID: "created-" + strconv.Itoa(i), Tenant: "tenant-created", Value: "value"}
			if err := table.Create(ctx, item); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Update", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			set := map[string]types.AttributeValue{"value": &types.AttributeValueMemberS{Value: "updated-" + strconv.Itoa(i)}}
			if err := table.Update(ctx, benchmarkKey{ID: "seed"}, set, nil, "id"); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("UpdateConditional", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		condition := dynamoxruntime.WriteOptions{Condition: &dynamoxruntime.Expression{
			Text:   "#value = :expected",
			Names:  map[string]string{"#value": "value"},
			Values: map[string]any{":expected": "initial"},
		}}
		set := map[string]types.AttributeValue{"value": &types.AttributeValueMemberS{Value: "initial"}}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := table.UpdateWithOptions(ctx, benchmarkKey{ID: "seed"}, set, nil, condition, "id"); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("UpdateReturningAllNew", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		set := map[string]types.AttributeValue{"value": &types.AttributeValueMemberS{Value: "returned"}}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item, err := table.UpdateReturning(ctx, benchmarkKey{ID: "seed"}, set, nil, dynamoxruntime.WriteOptions{}, "id")
			if err != nil || item.Value != "returned" {
				b.Fatalf("ALL_NEW update result: item=%#v err=%v", item, err)
			}
		}
	})

	b.Run("PutConditionalUpsert", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		item := benchmarkItem{ID: "seed", Tenant: "tenant-1", Value: "initial"}
		options := dynamoxruntime.WriteOptions{Condition: &dynamoxruntime.Expression{
			Text:   "#value = :expected",
			Names:  map[string]string{"#value": "value"},
			Values: map[string]any{":expected": "initial"},
		}}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := table.Put(ctx, item, options); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("QueryIndex", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			page, err := table.Query(ctx, dynamoxruntime.QueryRequest{
				IndexName:      "ByTenant",
				PartitionName:  "tenant",
				PartitionValue: "tenant-1",
			})
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != "seed" {
				b.Fatalf("Query result: page=%#v err=%v", page, err)
			}
		}
	})

	b.Run("QueryConsistentRead", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		request := dynamoxruntime.QueryRequest{
			PartitionName:  "id",
			PartitionValue: "seed",
			Page:           dynamoxruntime.PageRequest{ConsistentRead: true},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			page, err := table.Query(ctx, request)
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != "seed" {
				b.Fatalf("consistent Query result: page=%#v err=%v", page, err)
			}
		}
	})

	b.Run("Scan", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			page, err := table.Scan(ctx, dynamoxruntime.PageRequest{Limit: 10})
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != "seed" {
				b.Fatalf("Scan result: page=%#v err=%v", page, err)
			}
		}
	})

	b.Run("ScanFilteredConsistent", func(b *testing.B) {
		ctx, table := newBenchmarkTable(b)
		options := dynamoxruntime.ScanOptions{
			Page: dynamoxruntime.PageRequest{Limit: 10, ConsistentRead: true},
			Filter: &dynamoxruntime.Expression{
				Text:   "#tenant = :tenant",
				Names:  map[string]string{"#tenant": "tenant"},
				Values: map[string]any{":tenant": "tenant-1"},
			},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			page, err := table.ScanWithOptions(ctx, options)
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != "seed" {
				b.Fatalf("filtered consistent Scan result: page=%#v err=%v", page, err)
			}
		}
	})
}
