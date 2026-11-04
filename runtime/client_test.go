package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDynamo struct {
	get      *dynamodb.GetItemInput
	put      *dynamodb.PutItemInput
	update   *dynamodb.UpdateItemInput
	query    *dynamodb.QueryInput
	scan     *dynamodb.ScanInput
	read     *dynamodb.TransactGetItemsInput
	write    *dynamodb.TransactWriteItemsInput
	getOut   *dynamodb.GetItemOutput
	queryOut *dynamodb.QueryOutput
	scanOut  *dynamodb.ScanOutput
}

var _ DynamoAPI = (*dynamodb.Client)(nil)

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.get = in
	if f.getOut != nil {
		return f.getOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}
func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.put = in
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDynamo) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.update = in
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDynamo) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.query = in
	if f.queryOut != nil {
		return f.queryOut, nil
	}
	return &dynamodb.QueryOutput{}, nil
}
func (f *fakeDynamo) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.scan = in
	if f.scanOut != nil {
		return f.scanOut, nil
	}
	return &dynamodb.ScanOutput{}, nil
}
func (f *fakeDynamo) TransactGetItems(_ context.Context, in *dynamodb.TransactGetItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactGetItemsOutput, error) {
	f.read = in
	out := &dynamodb.TransactGetItemsOutput{}
	for range in.TransactItems {
		out.Responses = append(out.Responses, types.ItemResponse{Item: map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "x"}}})
	}
	return out, nil
}
func (f *fakeDynamo) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.write = in
	return &dynamodb.TransactWriteItemsOutput{}, nil
}
func (f *fakeDynamo) DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	return nil, errors.New("unused")
}
func (f *fakeDynamo) CreateTable(context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	return nil, errors.New("unused")
}

type testRecord struct {
	ID   string `dynamodbav:"id"`
	Name string `dynamodbav:"name"`
}

func testTable(db DynamoAPI) Table[testRecord, string] {
	return Table[testRecord, string]{DB: db, Name: "records", PartitionName: "id", Codec: Codec[testRecord]{Encode: func(v testRecord) (map[string]types.AttributeValue, error) { return attributevalue.MarshalMap(v) }, Decode: func(item map[string]types.AttributeValue) (testRecord, error) {
		var v testRecord
		err := attributevalue.UnmarshalMap(item, &v)
		return v, err
	}}, Key: func(k string) (map[string]types.AttributeValue, error) {
		return map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: k}}, nil
	}}
}

func TestCRUDAndPatchExpressions(t *testing.T) {
	db := &fakeDynamo{}
	table := testTable(db)
	ctx := context.Background()
	if err := table.Create(ctx, testRecord{ID: "a", Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	if db.put == nil || aws.ToString(db.put.ConditionExpression) != "attribute_not_exists(#pk)" {
		t.Fatalf("create must be conditional: %#v", db.put)
	}
	db.getOut = &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "a"}, "name": &types.AttributeValueMemberS{Value: "Ada"}}}
	got, ok, err := table.Get(ctx, "a")
	if err != nil || !ok || got.Name != "Ada" {
		t.Fatalf("Get = %#v, %v, %v", got, ok, err)
	}
	err = table.Update(ctx, "a", map[string]types.AttributeValue{"name": &types.AttributeValueMemberS{Value: "Grace"}}, []string{"obsolete"}, "id")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(aws.ToString(db.update.UpdateExpression), "SET") || !strings.Contains(aws.ToString(db.update.UpdateExpression), "REMOVE") {
		t.Fatalf("unexpected update expression %q", aws.ToString(db.update.UpdateExpression))
	}
	if err = table.Update(ctx, "a", map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "b"}}, nil, "id"); err == nil {
		t.Fatal("expected key update to fail")
	}
}

func TestQueryAndScanReturnPageCursors(t *testing.T) {
	db := &fakeDynamo{queryOut: &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{{"id": &types.AttributeValueMemberS{Value: "one"}}}, LastEvaluatedKey: map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "one"}}}, scanOut: &dynamodb.ScanOutput{LastEvaluatedKey: map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "next"}}}}
	table := testTable(db)
	page, err := table.Query(context.Background(), QueryRequest{IndexName: "ByName", PartitionName: "name", PartitionValue: "Ada", Page: PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "one" || page.Cursor == nil {
		t.Fatalf("unexpected query page: %#v", page)
	}
	if aws.ToString(db.query.IndexName) != "ByName" || aws.ToInt32(db.query.Limit) != 10 {
		t.Fatalf("query options not passed: %#v", db.query)
	}
	scan, err := table.Scan(context.Background(), PageRequest{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if scan.Cursor == nil || db.scan.FilterExpression != nil {
		t.Fatalf("scan should be paged and unfiltered: %#v", db.scan)
	}
}

func TestTransactionsComposeTypedOperations(t *testing.T) {
	db := &fakeDynamo{}
	table := testTable(db)
	var got testRecord
	read, err := Read(table, "x", &got)
	if err != nil {
		t.Fatal(err)
	}
	if err = TransactGet(context.Background(), db, read); err != nil {
		t.Fatal(err)
	}
	if got.ID != "x" || len(db.read.TransactItems) != 1 {
		t.Fatalf("transaction read not decoded: %#v", got)
	}
	put, err := table.PutOperation(testRecord{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	del, err := table.DeleteOperation("b")
	if err != nil {
		t.Fatal(err)
	}
	upd, err := table.UpdateOperation("c", map[string]types.AttributeValue{"name": &types.AttributeValueMemberS{Value: "n"}}, nil, "id")
	if err != nil {
		t.Fatal(err)
	}
	clear, err := table.UpdateOperation("d", nil, []string{"obsolete"}, "id")
	if err != nil {
		t.Fatal(err)
	}
	if err = TransactWrite(context.Background(), db, put, del, upd, clear); err != nil {
		t.Fatal(err)
	}
	if len(db.write.TransactItems) != 4 {
		t.Fatalf("expected 4 transaction writes, got %d", len(db.write.TransactItems))
	}
	if !strings.Contains(aws.ToString(db.write.TransactItems[3].Update.UpdateExpression), "REMOVE") {
		t.Fatalf("expected transactional remove expression, got %q", aws.ToString(db.write.TransactItems[3].Update.UpdateExpression))
	}
}
