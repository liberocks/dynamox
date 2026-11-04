package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestDynamoDBLocalConditionalCreateUpdateAndFilteredCursor(t *testing.T) {
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
	tableName := fmt.Sprintf("dynamox_runtime_it_%d", time.Now().UnixNano())
	_, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(tableName),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})
	if err := dynamodb.NewTableExistsWaiter(db).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	table := testTable(db)
	table.Name = tableName

	const contenders = 8
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := table.Create(ctx, testRecord{ID: "single-winner", Name: fmt.Sprintf("writer-%d", i)})
			if err == nil {
				winners.Add(1)
				return
			}
			var conditional *types.ConditionalCheckFailedException
			if !errors.As(err, &conditional) {
				errs <- fmt.Errorf("unexpected create error: %w", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := winners.Load(); got != 1 {
		t.Fatalf("conditional create admitted %d winners, want 1", got)
	}
	if err := table.Create(ctx, testRecord{ID: "webhook-receipt", Name: "evt-1"}); err != nil {
		t.Fatal(err)
	}
	expectLocalConditionalFailure(t, table.Create(ctx, testRecord{ID: "webhook-receipt", Name: "duplicate"}))
	receipt, found, err := table.GetWithOptions(ctx, "webhook-receipt", ReadOptions{ConsistentRead: true})
	if err != nil || !found || receipt.Name != "evt-1" {
		t.Fatalf("duplicate webhook receipt changed the original: receipt=%#v found=%v err=%v", receipt, found, err)
	}

	updatedName := "updated-once"
	updatedNameAV, err := attributevalue.Marshal(updatedName)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := table.UpdateReturning(ctx, "single-winner", map[string]types.AttributeValue{"name": updatedNameAV}, nil, WriteOptions{}, "id")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != updatedName {
		t.Fatalf("ALL_NEW returned %#v", updated)
	}
	conditionalKey := "conditional-writes"
	if err := table.Create(ctx, testRecord{ID: conditionalKey, Name: "before"}); err != nil {
		t.Fatal(err)
	}
	condition := func(expected string) WriteOptions {
		return WriteOptions{Condition: &Expression{Text: "#name = :expected", Names: map[string]string{"#name": "name"}, Values: map[string]any{":expected": expected}}}
	}
	expectLocalConditionalFailure(t, table.Put(ctx, testRecord{ID: conditionalKey, Name: "put"}, condition("wrong")))
	if err := table.Put(ctx, testRecord{ID: conditionalKey, Name: "put"}, condition("before")); err != nil {
		t.Fatal(err)
	}
	updatedAV, err := attributevalue.Marshal("updated")
	if err != nil {
		t.Fatal(err)
	}
	expectLocalConditionalFailure(t, table.UpdateWithOptions(ctx, conditionalKey, map[string]types.AttributeValue{"name": updatedAV}, nil, condition("wrong"), "id"))
	if err := table.UpdateWithOptions(ctx, conditionalKey, map[string]types.AttributeValue{"name": updatedAV}, nil, condition("put"), "id"); err != nil {
		t.Fatal(err)
	}
	expectLocalConditionalFailure(t, table.DeleteWithOptions(ctx, conditionalKey, condition("wrong")))
	if err := table.DeleteWithOptions(ctx, conditionalKey, condition("updated")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := table.GetWithOptions(ctx, "single-winner", ReadOptions{ConsistentRead: true}); err != nil || !ok {
		t.Fatalf("consistent read after update: found=%v err=%v", ok, err)
	}
	for _, id := range []string{"scan-extra-a", "scan-extra-b"} {
		if err := table.Create(ctx, testRecord{ID: id, Name: "extra"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := table.ScanWithOptions(ctx, ScanOptions{
		Page: PageRequest{Limit: 1, ConsistentRead: true},
		Filter: &Expression{
			Text:   "#id = :absent",
			Names:  map[string]string{"#id": "id"},
			Values: map[string]any{":absent": "not-present"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || len(page.Cursor) == 0 {
		t.Fatalf("filtered empty page must preserve DynamoDB's continuation key: %#v", page)
	}
	testDynamoDBLocalWebhookOrdering(t, ctx, db, tableName)
}

type webhookState struct {
	ID              string `dynamodbav:"id"`
	LastEventAt     string `dynamodbav:"lastEventAt"`
	RevokedByRefund bool   `dynamodbav:"revokedByRefund"`
	SubscriptionID  string `dynamodbav:"subscriptionId"`
}

func testDynamoDBLocalWebhookOrdering(t *testing.T, ctx context.Context, db DynamoAPI, tableName string) {
	t.Helper()
	codec := Codec[webhookState]{
		Encode: func(value webhookState) (map[string]types.AttributeValue, error) {
			return attributevalue.MarshalMap(value)
		},
		Decode: func(item map[string]types.AttributeValue) (webhookState, error) {
			var value webhookState
			err := attributevalue.UnmarshalMap(item, &value)
			return value, err
		},
	}
	table := Table[webhookState, string]{
		DB: db, Name: tableName, PartitionName: "id", Codec: codec,
		Key: func(key string) (map[string]types.AttributeValue, error) {
			return map[string]types.AttributeValue{"id": StringKey(key)}, nil
		},
	}
	state := webhookState{ID: "subscription-profile", LastEventAt: "2026-10-15T00:00:00Z", RevokedByRefund: true, SubscriptionID: "sub-1"}
	if err := table.Put(ctx, state); err != nil {
		t.Fatal(err)
	}
	condition := func(eventAt string, revokeByRefund bool) WriteOptions {
		return WriteOptions{Condition: &Expression{
			Text: "attribute_not_exists(lastEventAt) OR ((lastEventAt <= :eventAt OR :revokeByRefund = :true) AND (attribute_not_exists(revokedByRefund) OR revokedByRefund <> :true OR subscriptionId <> :subscriptionId OR :revokeByRefund = :true))",
			Values: map[string]any{
				":eventAt": eventAt, ":revokeByRefund": revokeByRefund, ":true": true, ":subscriptionId": "sub-1",
			},
		}}
	}
	expectLocalConditionalFailure(t, table.Put(ctx, webhookState{ID: state.ID, LastEventAt: "2026-10-14T00:00:00Z", SubscriptionID: "sub-1"}, condition("2026-10-14T00:00:00Z", false)))
	expectLocalConditionalFailure(t, table.Put(ctx, webhookState{ID: state.ID, LastEventAt: "2026-10-16T00:00:00Z", SubscriptionID: "sub-1"}, condition("2026-10-16T00:00:00Z", false)))
	unchanged, found, err := table.GetWithOptions(ctx, state.ID, ReadOptions{ConsistentRead: true})
	if err != nil || !found || unchanged.LastEventAt != state.LastEventAt || !unchanged.RevokedByRefund || unchanged.SubscriptionID != state.SubscriptionID {
		t.Fatalf("rejected webhook events changed entitlement state: state=%#v found=%v err=%v", unchanged, found, err)
	}
	if err := table.Put(ctx, webhookState{ID: state.ID, LastEventAt: "2026-10-13T00:00:00Z", RevokedByRefund: true, SubscriptionID: "sub-1"}, condition("2026-10-13T00:00:00Z", true)); err != nil {
		t.Fatalf("refund override should satisfy the Overmost ordering condition: %v", err)
	}
	got, found, err := table.GetWithOptions(ctx, state.ID, ReadOptions{ConsistentRead: true})
	if err != nil || !found || got.LastEventAt != "2026-10-13T00:00:00Z" || !got.RevokedByRefund {
		t.Fatalf("refund event ordering result: state=%#v found=%v err=%v", got, found, err)
	}
}

func expectLocalConditionalFailure(t *testing.T, err error) {
	t.Helper()
	var conditional *types.ConditionalCheckFailedException
	if !errors.As(err, &conditional) {
		t.Fatalf("expected conditional write failure, got %v", err)
	}
}
