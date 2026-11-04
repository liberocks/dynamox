package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type coverageDB struct {
	*fakeDynamo
	err          error
	getOut       *dynamodb.GetItemOutput
	updateOut    *dynamodb.UpdateItemOutput
	nilUpdateOut bool
	queryOut     *dynamodb.QueryOutput
	scanOut      *dynamodb.ScanOutput
	readOut      *dynamodb.TransactGetItemsOutput
}

func (d *coverageDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	d.fakeDynamo.get = in
	if d.err != nil {
		return nil, d.err
	}
	if d.getOut != nil {
		return d.getOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}
func (d *coverageDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	d.fakeDynamo.put = in
	if d.err != nil {
		return nil, d.err
	}
	return &dynamodb.PutItemOutput{}, nil
}
func (d *coverageDB) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	if d.err != nil {
		return nil, d.err
	}
	return &dynamodb.DeleteItemOutput{}, nil
}
func (d *coverageDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	d.fakeDynamo.update = in
	if d.err != nil {
		return nil, d.err
	}
	if d.nilUpdateOut {
		return nil, nil
	}
	if d.updateOut != nil {
		return d.updateOut, nil
	}
	return &dynamodb.UpdateItemOutput{}, nil
}
func (d *coverageDB) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	d.fakeDynamo.query = in
	if d.err != nil {
		return nil, d.err
	}
	if d.queryOut != nil {
		return d.queryOut, nil
	}
	return &dynamodb.QueryOutput{}, nil
}
func (d *coverageDB) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	d.fakeDynamo.scan = in
	if d.err != nil {
		return nil, d.err
	}
	if d.scanOut != nil {
		return d.scanOut, nil
	}
	return &dynamodb.ScanOutput{}, nil
}
func (d *coverageDB) TransactGetItems(_ context.Context, in *dynamodb.TransactGetItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactGetItemsOutput, error) {
	d.fakeDynamo.read = in
	if d.err != nil {
		return nil, d.err
	}
	if d.readOut != nil {
		return d.readOut, nil
	}
	out := &dynamodb.TransactGetItemsOutput{}
	for range in.TransactItems {
		out.Responses = append(out.Responses, types.ItemResponse{Item: map[string]types.AttributeValue{"id": StringKey("ok")}})
	}
	return out, nil
}
func (d *coverageDB) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	d.fakeDynamo.write = in
	if d.err != nil {
		return nil, d.err
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func brokenKeyTable(db DynamoAPI) Table[testRecord, string] {
	t := testTable(db)
	t.Key = func(string) (map[string]types.AttributeValue, error) { return nil, errors.New("key") }
	return t
}
func brokenCodecTable(db DynamoAPI) Table[testRecord, string] {
	t := testTable(db)
	t.Codec.Encode = func(testRecord) (map[string]types.AttributeValue, error) { return nil, errors.New("encode") }
	t.Codec.Decode = func(map[string]types.AttributeValue) (testRecord, error) { return testRecord{}, errors.New("decode") }
	return t
}
func item(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"id": StringKey(id)}
}

type badMarshal struct{}

func (badMarshal) MarshalDynamoDBAttributeValue() (types.AttributeValue, error) {
	return nil, errors.New("marshal")
}

func TestTableErrorAndBoundaryBranches(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db")
	if _, ok := StringKey("x").(*types.AttributeValueMemberS); !ok {
		t.Fatal("StringKey")
	}
	if IntKey(12).(*types.AttributeValueMemberN).Value != "12" {
		t.Fatal("IntKey")
	}
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.FixedZone("west", -3600))
	if av, err := marshalValue(now); err != nil || av.(*types.AttributeValueMemberS).Value != "2025-01-02T04:04:05Z" {
		t.Fatalf("time marshal: %v %v", av, err)
	}
	if av, err := marshalValue([]byte{1}); err != nil || av.(*types.AttributeValueMemberB).Value[0] != 1 {
		t.Fatalf("bytes marshal: %v %v", av, err)
	}
	if _, err := marshalValue(badMarshal{}); err == nil {
		t.Fatal("unsupported value should fail")
	}
	d := &coverageDB{fakeDynamo: &fakeDynamo{}}
	table := testTable(d)
	if _, _, err := brokenKeyTable(d).Get(ctx, "x"); err == nil {
		t.Fatal("Get key error")
	}
	d.err = boom
	if _, _, err := table.Get(ctx, "x"); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := table.Create(ctx, testRecord{}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := table.Delete(ctx, "x"); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	d.err = nil
	d.getOut = &dynamodb.GetItemOutput{}
	if _, ok, err := table.Get(ctx, "x"); err != nil || ok {
		t.Fatalf("missing item: %v %v", ok, err)
	}
	d.getOut = &dynamodb.GetItemOutput{Item: item("x")}
	bad := testTable(d)
	bad.Codec.Decode = func(map[string]types.AttributeValue) (testRecord, error) { return testRecord{}, boom }
	if _, _, err := bad.Get(ctx, "x"); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if err := brokenCodecTable(d).Create(ctx, testRecord{}); err == nil {
		t.Fatal("Create encode error")
	}
	sorted := table
	sorted.SortName = "sk"
	if err := sorted.Create(ctx, testRecord{}); err != nil {
		t.Fatal(err)
	}
	if d.put.ExpressionAttributeNames["#sk"] != "sk" {
		t.Fatal("sort key condition")
	}
	if err := brokenKeyTable(d).Delete(ctx, "x"); err == nil {
		t.Fatal("Delete key error")
	}
	if err := table.Update(ctx, "x", nil, nil); err == nil {
		t.Fatal("empty update")
	}
	if err := table.Update(ctx, "x", map[string]types.AttributeValue{"a": StringKey("v")}, []string{"a"}); err == nil {
		t.Fatal("overlap")
	}
	if err := table.Update(ctx, "x", nil, []string{"a", "a"}); err == nil {
		t.Fatal("duplicate remove")
	}
	if err := brokenKeyTable(d).Update(ctx, "x", map[string]types.AttributeValue{"a": StringKey("v")}, nil); err == nil {
		t.Fatal("update key error")
	}
	if err := table.Update(ctx, "x", nil, []string{"id"}, "id"); err == nil {
		t.Fatal("remove key")
	}
	if err := table.Update(ctx, "x", map[string]types.AttributeValue{"a": nil}, nil); err == nil {
		t.Fatal("nil value")
	}
	d.err = boom
	if err := table.Update(ctx, "x", nil, []string{"a"}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	d.err = nil
	if err := table.Update(ctx, "x", nil, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if d.update.ExpressionAttributeValues != nil {
		t.Fatal("remove-only update values")
	}
	if err := table.Update(ctx, "x", map[string]types.AttributeValue{"a": StringKey("v")}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestQueryAndScanAllBranches(t *testing.T) {
	ctx := context.Background()
	d := &coverageDB{fakeDynamo: &fakeDynamo{}}
	table := testTable(d)
	if _, err := table.Query(ctx, QueryRequest{}); err == nil {
		t.Fatal("missing partition")
	}
	if _, err := table.Query(ctx, QueryRequest{PartitionName: "pk", PartitionValue: badMarshal{}}); err == nil {
		t.Fatal("partition marshal")
	}
	if _, err := table.Query(ctx, QueryRequest{PartitionName: "pk", PartitionValue: "x", Sort: &Condition{Operator: Equal, Value: "x"}}); err == nil {
		t.Fatal("sort key omitted")
	}
	if _, err := table.Query(ctx, QueryRequest{PartitionName: "pk", PartitionValue: "x", SortName: "sk", Sort: &Condition{Operator: Equal, Value: badMarshal{}}}); err == nil {
		t.Fatal("sort marshal")
	}
	for _, op := range []Operator{Equal, Less, LessEqual, Greater, GreaterEqual, BeginsWith} {
		page, err := table.Query(ctx, QueryRequest{PartitionName: "pk", PartitionValue: "x", SortName: "sk", Sort: &Condition{Operator: op, Value: "v"}, Page: PageRequest{Limit: 2, Cursor: item("c")}, IndexName: "ByPK"})
		if err != nil {
			t.Fatal(op, err)
		}
		if page.Items == nil || d.query.IndexName == nil || d.query.ScanIndexForward != nil {
			t.Fatal("query setup", op)
		}
	}
	if _, err := table.Query(ctx, QueryRequest{PartitionName: "pk", PartitionValue: "x", SortName: "sk", Sort: &Condition{Operator: "???", Value: "v"}}); err == nil {
		t.Fatal("unsupported operator")
	}
	d.err = errors.New("query")
	if _, err := table.Query(ctx, QueryRequest{PartitionName: "pk", PartitionValue: "x"}); err == nil {
		t.Fatal("query API error")
	}
	d.err = nil
	bad := testTable(d)
	bad.Codec.Decode = func(map[string]types.AttributeValue) (testRecord, error) { return testRecord{}, errors.New("decode") }
	d.queryOut = &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{item("x")}}
	if _, err := bad.Query(ctx, QueryRequest{PartitionName: "pk", PartitionValue: "x"}); err == nil {
		t.Fatal("query decode")
	}
	d.queryOut = nil
	d.err = errors.New("scan")
	if _, err := table.Scan(ctx, PageRequest{Limit: 4, Cursor: item("c")}); err == nil {
		t.Fatal("scan API error")
	}
	d.err = nil
	d.scanOut = &dynamodb.ScanOutput{Items: []map[string]types.AttributeValue{item("x")}}
	if _, err := bad.Scan(ctx, PageRequest{}); err == nil {
		t.Fatal("scan decode")
	}
	d.scanOut = nil
	d.scanOut = &dynamodb.ScanOutput{Items: []map[string]types.AttributeValue{item("x")}}
	if page, err := table.Scan(ctx, PageRequest{}); err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
}

func TestOperationAndTransactionErrorBranches(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db")
	d := &coverageDB{fakeDynamo: &fakeDynamo{}}
	table := testTable(d)
	if _, err := Read(table, "x", nil); err == nil {
		t.Fatal("nil read destination")
	}
	if _, err := Read(brokenKeyTable(d), "x", new(testRecord)); err == nil {
		t.Fatal("read key")
	}
	rd, err := Read(table, "x", new(testRecord))
	if err != nil {
		t.Fatal(err)
	}
	if err := rd.Decode(item("decoded")); err != nil {
		t.Fatal(err)
	}
	failing := rd
	failing.Decode = func(map[string]types.AttributeValue) error { return boom }
	if err := failing.Decode(item("x")); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := brokenCodecTable(d).PutOperation(testRecord{}); err == nil {
		t.Fatal("put op encode")
	}
	if _, err := brokenKeyTable(d).DeleteOperation("x"); err == nil {
		t.Fatal("delete op key")
	}
	if _, err := table.UpdateOperation("x", nil, nil); err == nil {
		t.Fatal("empty update op")
	}
	if _, err := brokenKeyTable(d).UpdateOperation("x", map[string]types.AttributeValue{"x": StringKey("x")}, nil); err == nil {
		t.Fatal("update op key")
	}
	if err := TransactGet(ctx, d); err == nil {
		t.Fatal("empty transaction get")
	}
	many := make([]ReadItem, 101)
	if err := TransactGet(ctx, d, many...); err == nil {
		t.Fatal("transaction get limit")
	}
	d.err = boom
	if err := TransactGet(ctx, d, rd); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	d.err = nil
	d.readOut = &dynamodb.TransactGetItemsOutput{}
	if err := TransactGet(ctx, d, rd); err == nil {
		t.Fatal("response count")
	}
	d.readOut = &dynamodb.TransactGetItemsOutput{Responses: []types.ItemResponse{{}}}
	if err := TransactGet(ctx, d, rd); err == nil {
		t.Fatal("missing response item")
	}
	d.readOut = &dynamodb.TransactGetItemsOutput{Responses: []types.ItemResponse{{Item: item("x")}}}
	if err := TransactGet(ctx, d, failing); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	d.readOut = nil
	if err := TransactGet(ctx, d, rd); err != nil {
		t.Fatal(err)
	}
	if err := TransactWrite(ctx, d); err == nil {
		t.Fatal("empty transaction write")
	}
	writes := make([]WriteItem, 101)
	if err := TransactWrite(ctx, d, writes...); err == nil {
		t.Fatal("transaction write limit")
	}
	d.err = boom
	if err := TransactWrite(ctx, d, WriteItem{Put: item("x")}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	d.err = nil
	cases := []WriteItem{{}, {Table: "x", Set: map[string]types.AttributeValue{}}, {Table: "x", Set: map[string]types.AttributeValue{"id": StringKey("x")}, KeyNames: []string{"id"}}, {Table: "x", Set: map[string]types.AttributeValue{"x": StringKey("x")}, Remove: []string{"x"}}, {Table: "x", Remove: []string{"x", "x"}}, {Table: "x", Remove: []string{"id"}, KeyNames: []string{"id"}}}
	for _, w := range cases {
		if err := TransactWrite(ctx, d, w); err == nil {
			t.Fatalf("expected invalid update: %#v", w)
		}
	}
	upd := WriteItem{Table: "x", Key: item("x"), Set: map[string]types.AttributeValue{"b": StringKey("2"), "a": StringKey("1")}, Remove: []string{"gone"}}
	if err := TransactWrite(ctx, d, WriteItem{Table: "x", Put: item("p")}, WriteItem{Table: "x", Key: item("d")}, upd, WriteItem{Table: "x", Remove: []string{"gone"}}); err != nil {
		t.Fatal(err)
	}
	if len(d.write.TransactItems) != 4 || !strings.Contains(*d.write.TransactItems[2].Update.UpdateExpression, "REMOVE") {
		t.Fatal("transaction operations")
	}
}

func TestNextSerialUsesAtomicDynamoDBCounter(t *testing.T) {
	d := &coverageDB{fakeDynamo: &fakeDynamo{}, updateOut: &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{"value": &types.AttributeValueMemberN{Value: "27"}}}}
	got, err := NextSerial(context.Background(), d, "users")
	if err != nil || got != 27 {
		t.Fatalf("serial %d %v", got, err)
	}
	in := d.update
	if aws.ToString(in.TableName) != SequenceTableName || aws.ToString(in.UpdateExpression) != "ADD #value :one" || in.ReturnValues != types.ReturnValueUpdatedNew || in.Key["name"].(*types.AttributeValueMemberS).Value != "users" || in.ExpressionAttributeValues[":one"].(*types.AttributeValueMemberN).Value != "1" {
		t.Fatalf("unexpected serial update: %#v", in)
	}
	d.err = errors.New("offline")
	if _, err := NextSerial(context.Background(), d, "users"); err == nil {
		t.Fatal("DynamoDB error")
	}
	d.err = nil
	d.nilUpdateOut = true
	if _, err := NextSerial(context.Background(), d, "users"); err == nil {
		t.Fatal("nil response")
	}
	d.nilUpdateOut = false
	d.updateOut = &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{"value": StringKey("27")}}
	if _, err := NextSerial(context.Background(), d, "users"); err == nil {
		t.Fatal("non-numeric counter")
	}
	d.updateOut = &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{"value": &types.AttributeValueMemberN{Value: "not-a-number"}}}
	if _, err := NextSerial(context.Background(), d, "users"); err == nil {
		t.Fatal("malformed counter")
	}
}
