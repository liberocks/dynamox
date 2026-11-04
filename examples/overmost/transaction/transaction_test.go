package transaction

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/examples/overmost/admin"
	"github.com/liberocks/dynamox/examples/overmost/learner"
	"github.com/liberocks/dynamox/runtime"
)

type publishFailureDB struct {
	runtime.DynamoAPI
	err error
}

func (d publishFailureDB) TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	return nil, d.err
}

func TestPublishReturnsTransactionResult(t *testing.T) {
	ctx := context.Background()
	adminClient := admin.NewClient(publishFailureDB{})
	learnerClient := learner.NewClient(publishFailureDB{})
	key := admin.DraftKey{PK: "EXAM#one", SK: "DRAFT#one"}
	content := learner.Content{PK: key.PK, SK: "QUIZ#one", Status: "published", Sections: []learner.Section{}, ProviderPayload: runtime.JSON(map[string]any{})}
	if err := Publish(ctx, adminClient, learnerClient, key, content); err != nil {
		t.Fatalf("successful transaction returned an error: %v", err)
	}
	want := &types.TransactionCanceledException{}
	adminClient = admin.NewClient(publishFailureDB{err: want})
	learnerClient = learner.NewClient(publishFailureDB{err: want})
	err := Publish(ctx, adminClient, learnerClient, key, content)
	var got *types.TransactionCanceledException
	if !errors.As(err, &got) || got != want {
		t.Fatalf("transaction cancellation type was not preserved: %v", err)
	}
}

func TestSeparateSchemasKeepOverridesAndBuildMixedOperations(t *testing.T) {
	db := runtime.DynamoAPI(nil)
	adminClient := admin.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"admin-drafts": "admin-prod"}})
	learnerClient := learner.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"learner-content": "learner-prod"}})
	status := "published"
	update, err := adminClient.UpdateDraftOperationWithOptions(admin.DraftKey{PK: "EXAM#ielts", SK: "DRAFT#quiz-1"}, admin.DraftUpdate{SetStatus: &status}, runtime.WriteOptions{Condition: &runtime.Expression{Text: "#status = :unpublished", Names: map[string]string{"#status": "status"}, Values: map[string]any{":unpublished": "unpublished"}}})
	if err != nil {
		t.Fatal(err)
	}
	put, err := learnerClient.CreateContentOperation(learner.Content{PK: "EXAM#ielts", SK: "QUIZ#quiz-1", Status: "published"})
	if err != nil {
		t.Fatal(err)
	}
	if update.Table != "admin-prod" || put.Table != "learner-prod" {
		t.Fatalf("physical table overrides were lost: %q %q", update.Table, put.Table)
	}
	if update.Condition == nil || !put.CreateOnly {
		t.Fatalf("expected a conditional publish operation and create-only content operation: %#v %#v", update, put)
	}
}
