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
	"github.com/liberocks/dynamox/examples/overmost/admin"
	"github.com/liberocks/dynamox/examples/overmost/learner"
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
	adminTable, learnerTable := "dynamox_admin_it_"+suffix, "dynamox_learner_it_"+suffix
	for _, name := range []string{adminTable, learnerTable} {
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

	adminClient := admin.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"admin-drafts": adminTable}})
	learnerClient := learner.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"learner-content": learnerTable}})
	const pk = "EXAM#ielts"
	key := admin.DraftKey{PK: pk, SK: "DRAFT#quiz-1"}
	draft, err := adminClient.CreateDraft(ctx, admin.Draft{PK: key.PK, SK: key.SK, Status: "unpublished"})
	if err != nil || draft.Status != "unpublished" {
		t.Fatalf("seed draft through overridden admin client: draft=%#v err=%v", draft, err)
	}
	content := learner.Content{PK: pk, SK: "QUIZ#quiz-1", Status: "published", Sections: []learner.Section{}, ProviderPayload: runtime.JSON(map[string]any{"source": "integration"})}
	if err := Publish(ctx, adminClient, learnerClient, key, content); err != nil {
		t.Fatal(err)
	}
	updated, found, err := adminClient.GetDraftWithOptions(ctx, key, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || updated.Status != "published" {
		t.Fatalf("draft update missing after transaction: draft=%#v found=%v err=%v", updated, found, err)
	}
	created, found, err := learnerClient.GetContentWithOptions(ctx, learner.ContentKey{PK: content.PK, SK: content.SK}, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || created.Status != "published" {
		t.Fatalf("content creation missing after transaction: content=%#v found=%v err=%v", created, found, err)
	}

	// A non-publishable draft must cancel the complete transaction, leaving both
	// the draft and content rows unchanged.
	staleKey := admin.DraftKey{PK: pk, SK: "DRAFT#quiz-2"}
	if _, err := adminClient.CreateDraft(ctx, admin.Draft{PK: staleKey.PK, SK: staleKey.SK, Status: "archived"}); err != nil {
		t.Fatal(err)
	}
	content.SK = "QUIZ#quiz-2"
	err = Publish(ctx, adminClient, learnerClient, staleKey, content)
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
	unchanged, found, err := adminClient.GetDraftWithOptions(ctx, staleKey, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || unchanged.Status != "archived" {
		t.Fatalf("failed cross-client transaction changed its draft: draft=%#v found=%v err=%v", unchanged, found, err)
	}
	_, found, err = learnerClient.GetContentWithOptions(ctx, learner.ContentKey{PK: content.PK, SK: content.SK}, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || found {
		t.Fatalf("failed cross-client transaction created its content item: found=%v err=%v", found, err)
	}
}
