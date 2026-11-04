package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type expressionMarshalFailure struct{}

func (expressionMarshalFailure) MarshalDynamoDBAttributeValue() (types.AttributeValue, error) {
	return nil, errors.New("expression marshal failure")
}

type optionDB struct {
	*fakeDynamo
	del       *dynamodb.DeleteItemInput
	updateOut *dynamodb.UpdateItemOutput
	err       error
	gets      int
}

func (d *optionDB) GetItem(ctx context.Context, in *dynamodb.GetItemInput, o ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	d.gets++
	return d.fakeDynamo.GetItem(ctx, in, o...)
}
func (d *optionDB) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	d.del = in
	return &dynamodb.DeleteItemOutput{}, d.err
}
func (d *optionDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	d.update = in
	if d.err != nil {
		return nil, d.err
	}
	if d.updateOut != nil {
		return d.updateOut, nil
	}
	return &dynamodb.UpdateItemOutput{}, nil
}
func (d *optionDB) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	d.write = in
	return nil, d.err
}

func TestOptionsConditionsPlaceholderMergingAndReturningUpdate(t *testing.T) {
	db := &optionDB{fakeDynamo: &fakeDynamo{}, updateOut: &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{"id": StringKey("a"), "name": StringKey("new")}}}
	table := testTable(db)
	ctx := context.Background()
	condition := &Expression{Text: "#pk = :status", Names: map[string]string{"#pk": "status"}, Values: map[string]any{":status": "pending"}}
	if err := table.CreateWithOptions(ctx, testRecord{ID: "a"}, WriteOptions{Condition: condition}); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(db.put.ConditionExpression) != "attribute_not_exists(#pk_1) AND (#pk = :status)" || db.put.ExpressionAttributeNames["#pk_1"] != "id" {
		t.Fatalf("create condition collision: %#v", db.put)
	}
	if err := table.Put(ctx, testRecord{ID: "a"}, WriteOptions{Condition: condition}); err != nil {
		t.Fatal(err)
	}
	createOperation, err := table.CreateOperation(testRecord{ID: "op"}, WriteOptions{Condition: condition})
	if err != nil || createOperation.Condition.Values[":status"] != "pending" {
		t.Fatalf("conditional create operation values were not preserved: %#v err=%v", createOperation, err)
	}
	if aws.ToString(db.put.ConditionExpression) != "#pk = :status" {
		t.Fatalf("upsert condition missing: %#v", db.put)
	}
	set := map[string]types.AttributeValue{"name": StringKey("new")}
	collision := &Expression{Text: "attribute_exists(#dx_f0) AND #dx_f0 = :dx_v0", Names: map[string]string{"#dx_f0": "status"}, Values: map[string]any{":dx_v0": "stale"}}
	got, err := table.UpdateReturning(ctx, "a", set, nil, WriteOptions{Condition: collision}, "id")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "new" || db.update.ReturnValues != types.ReturnValueAllNew || db.gets != 0 {
		t.Fatalf("returning update did not use one ALL_NEW request: model=%#v update=%#v gets=%d", got, db.update, db.gets)
	}
	if !strings.Contains(aws.ToString(db.update.UpdateExpression), "#dx_f0_1") || db.update.ExpressionAttributeValues[":dx_v0_1"] == nil {
		t.Fatalf("update placeholders collided with condition: %#v", db.update)
	}
	if err := table.DeleteWithOptions(ctx, "a", WriteOptions{Condition: condition}); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(db.del.ConditionExpression) != "#pk = :status" || db.del.ExpressionAttributeValues[":status"] == nil {
		t.Fatalf("delete condition missing: %#v", db.del)
	}
}

func TestReadFilteringAndConsistentIndexRules(t *testing.T) {
	db := &optionDB{fakeDynamo: &fakeDynamo{scanOut: &dynamodb.ScanOutput{LastEvaluatedKey: map[string]types.AttributeValue{"id": StringKey("next")}}}}
	table := testTable(db)
	ctx := context.Background()
	_, _, err := table.GetWithOptions(ctx, "a", ReadOptions{ConsistentRead: true})
	if err != nil || db.get.ConsistentRead == nil || !*db.get.ConsistentRead {
		t.Fatalf("consistent get was not passed: %#v %v", db.get, err)
	}
	_, err = table.Query(ctx, QueryRequest{IndexName: "ByName", PartitionName: "name", PartitionValue: "x", GlobalIndex: true, Page: PageRequest{ConsistentRead: true}})
	if err == nil || !strings.Contains(err.Error(), "GSIs") {
		t.Fatalf("expected consistent GSI query rejection, got %v", err)
	}
	_, err = table.Query(ctx, QueryRequest{IndexName: "ByStatus", PartitionName: "status", PartitionValue: "x", Page: PageRequest{ConsistentRead: true}})
	if err != nil || db.query.ConsistentRead == nil || !*db.query.ConsistentRead {
		t.Fatalf("consistent LSI query failed: %#v %v", db.query, err)
	}
	filter := &Expression{Text: "#kind = :kind", Names: map[string]string{"#kind": "entityType"}, Values: map[string]any{":kind": "USER"}}
	page, err := table.ScanWithOptions(ctx, ScanOptions{Page: PageRequest{Limit: 2, ConsistentRead: true}, Filter: filter})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Cursor == nil || db.scan.FilterExpression == nil || db.scan.ExpressionAttributeValues[":kind"] == nil || !aws.ToBool(db.scan.ConsistentRead) {
		t.Fatalf("filtered empty page lost cursor or options: %#v %#v", page, db.scan)
	}
}

func TestConditionalTransactionKeepsAWSFailureType(t *testing.T) {
	want := &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{{Code: aws.String("ConditionalCheckFailed")}}}
	db := &optionDB{fakeDynamo: &fakeDynamo{}, err: want}
	table := testTable(db)
	op, err := table.CreateOperation(testRecord{ID: "new"}, WriteOptions{Condition: &Expression{Text: "attribute_exists(#status)", Names: map[string]string{"#status": "status"}}})
	if err != nil {
		t.Fatal(err)
	}
	err = TransactWrite(context.Background(), db, op)
	var got *types.TransactionCanceledException
	if !errors.As(err, &got) || got != want {
		t.Fatalf("transaction error type was not preserved: %v", err)
	}
	if put := db.write.TransactItems[0].Put; aws.ToString(put.ConditionExpression) != "attribute_not_exists(#dx_create_pk) AND (attribute_exists(#status))" {
		t.Fatalf("transaction create was not conditional: %#v", put)
	}
	db.err = nil
	deleteOperation, err := table.DeleteOperationWithOptions("old", WriteOptions{Condition: &Expression{Text: "#status = :status", Names: map[string]string{"#status": "status"}, Values: map[string]any{":status": "stale"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := TransactWrite(context.Background(), db, deleteOperation); err != nil {
		t.Fatal(err)
	}
	if delete := db.write.TransactItems[0].Delete; delete == nil || delete.ExpressionAttributeValues[":status"] == nil {
		t.Fatalf("conditional transactional delete did not carry expression values: %#v", db.write)
	}
	sorted := table
	sorted.SortName = "sortKey"
	operation, err := sorted.CreateOperation(testRecord{ID: "new"}, WriteOptions{})
	if err != nil || operation.Condition.Names["#dx_create_pk"] != "sortKey" {
		t.Fatalf("composite create operation should use the sort key for its absence condition: %#v err=%v", operation, err)
	}
}

func TestConditionValueEncodingErrorsAreReturned(t *testing.T) {
	table := testTable(&optionDB{fakeDynamo: &fakeDynamo{}})
	condition := WriteOptions{Condition: &Expression{Text: "#status = :status", Names: map[string]string{"#status": "status"}, Values: map[string]any{":status": make(chan int)}}}
	if err := table.CreateWithOptions(context.Background(), testRecord{ID: "x"}, condition); err == nil {
		t.Fatal("expected create condition value encoding error")
	}
	if err := table.Put(context.Background(), testRecord{ID: "x"}, condition); err == nil {
		t.Fatal("expected condition value encoding error")
	}
	if err := table.DeleteWithOptions(context.Background(), "x", condition); err == nil {
		t.Fatal("expected delete condition value encoding error")
	}
	if err := table.UpdateWithOptions(context.Background(), "x", map[string]types.AttributeValue{"name": StringKey("x")}, nil, condition); err == nil {
		t.Fatal("expected update condition value encoding error")
	}
	if _, err := table.ScanWithOptions(context.Background(), ScanOptions{Filter: condition.Condition}); err == nil {
		t.Fatal("expected scan filter value encoding error")
	}
	if _, err := table.CreateOperation(testRecord{ID: "x"}, condition); err == nil {
		t.Fatal("expected create operation condition value encoding error")
	}
	if _, err := table.CreateOperation(testRecord{ID: "x"}, WriteOptions{Condition: &Expression{Text: "#status = :status", Values: map[string]any{":status": expressionMarshalFailure{}}}}); err == nil {
		t.Fatal("expected DynamoDB expression marshaler error")
	}
	if _, err := table.DeleteOperationWithOptions("x", condition); err == nil {
		t.Fatal("expected delete operation condition value encoding error")
	}
	if _, err := table.UpdateOperationWithOptions("x", map[string]types.AttributeValue{"name": StringKey("x")}, nil, condition); err == nil {
		t.Fatal("expected update operation condition value encoding error")
	}
	if _, err := table.PutOperationWithOptions(testRecord{ID: "x"}, condition); err == nil {
		t.Fatal("expected transaction condition value encoding error")
	}
	badWrites := []WriteItem{
		{Table: "records", Put: map[string]types.AttributeValue{"id": StringKey("x")}, Condition: condition.Condition},
		{Table: "records", Key: map[string]types.AttributeValue{"id": StringKey("x")}, Set: map[string]types.AttributeValue{"name": StringKey("x")}, Condition: condition.Condition},
		{Table: "records", Key: map[string]types.AttributeValue{"id": StringKey("x")}, Condition: condition.Condition},
	}
	for _, item := range badWrites {
		if err := TransactWrite(context.Background(), table.DB, item); err == nil {
			t.Fatalf("expected transaction condition value encoding error: %#v", item)
		}
	}
}

func TestClientOptionsTableNameAndOperationErrorInputs(t *testing.T) {
	options := ClientOptions{TableNames: map[string]string{"records": "records-test"}}
	if got := options.TableName("records"); got != "records-test" {
		t.Fatalf("override table name %q", got)
	}
	if got := options.TableName("other"); got != "other" {
		t.Fatalf("default table name %q", got)
	}
	if got := (ClientOptions{}).TableName("default"); got != "default" {
		t.Fatalf("empty options table name %q", got)
	}
	ctx := context.Background()
	db := &fakeDynamo{}
	table := testTable(db)
	brokenCodec := table
	brokenCodec.Codec.Encode = func(testRecord) (map[string]types.AttributeValue, error) { return nil, errors.New("encode") }
	if err := brokenCodec.CreateWithOptions(ctx, testRecord{}, WriteOptions{}); err == nil {
		t.Fatal("Create should return model encoding error")
	}
	if err := brokenCodec.Put(ctx, testRecord{}); err == nil {
		t.Fatal("Put should return model encoding error")
	}
	if _, err := brokenCodec.CreateOperation(testRecord{}, WriteOptions{}); err == nil {
		t.Fatal("CreateOperation should return model encoding error")
	}
	keyErrTable := table
	keyErrTable.Key = func(string) (map[string]types.AttributeValue, error) { return nil, errors.New("key") }
	if err := keyErrTable.DeleteWithOptions(ctx, "x", WriteOptions{}); err == nil {
		t.Fatal("Delete should return key encoding error")
	}
	if _, err := keyErrTable.DeleteOperationWithOptions("x", WriteOptions{}); err == nil {
		t.Fatal("delete operation should return key encoding error")
	}
	if _, err := keyErrTable.UpdateOperationWithOptions("x", map[string]types.AttributeValue{"name": StringKey("x")}, nil, WriteOptions{}); err == nil {
		t.Fatal("update operation should return key encoding error")
	}
	if _, err := table.UpdateOperationWithOptions("x", nil, nil, WriteOptions{}); err == nil {
		t.Fatal("update operation should reject empty changes")
	}
	if err := TransactWrite(ctx, db, WriteItem{}); err == nil {
		t.Fatal("transaction should reject invalid operation")
	}
	if err := TransactWrite(ctx, db, WriteItem{Table: "records", Key: map[string]types.AttributeValue{"id": StringKey("x")}, Set: map[string]types.AttributeValue{"id": StringKey("y")}, KeyNames: []string{"id"}}); err == nil {
		t.Fatal("transaction should reject key mutation")
	}
}
