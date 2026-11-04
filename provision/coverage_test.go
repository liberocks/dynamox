package provision

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

type scriptedAPI struct {
	describes      []*dynamodb.DescribeTableOutput
	describeErrs   []error
	createErr      error
	createInput    *dynamodb.CreateTableInput
	updateErr      error
	updateInputs   []*dynamodb.UpdateTableInput
	ttlDescription *types.TimeToLiveDescription
	ttlErr         error
	ttlUpdateErr   error
	ttlUpdates     []*dynamodb.UpdateTimeToLiveInput
	describeCalls  int
}

func (f *scriptedAPI) DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	i := f.describeCalls
	f.describeCalls++
	if i < len(f.describeErrs) && f.describeErrs[i] != nil {
		return nil, f.describeErrs[i]
	}
	if len(f.describes) == 0 {
		return &dynamodb.DescribeTableOutput{}, nil
	}
	if i >= len(f.describes) {
		i = len(f.describes) - 1
	}
	return f.describes[i], nil
}
func (f *scriptedAPI) CreateTable(_ context.Context, in *dynamodb.CreateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	f.createInput = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &dynamodb.CreateTableOutput{}, nil
}
func (f *scriptedAPI) UpdateTable(_ context.Context, in *dynamodb.UpdateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateTableOutput, error) {
	f.updateInputs = append(f.updateInputs, in)
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &dynamodb.UpdateTableOutput{}, nil
}
func (f *scriptedAPI) DescribeTimeToLive(context.Context, *dynamodb.DescribeTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTimeToLiveOutput, error) {
	if f.ttlErr != nil {
		return nil, f.ttlErr
	}
	return &dynamodb.DescribeTimeToLiveOutput{TimeToLiveDescription: f.ttlDescription}, nil
}
func (f *scriptedAPI) UpdateTimeToLive(_ context.Context, in *dynamodb.UpdateTimeToLiveInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateTimeToLiveOutput, error) {
	f.ttlUpdates = append(f.ttlUpdates, in)
	if f.ttlUpdateErr != nil {
		return nil, f.ttlUpdateErr
	}
	return &dynamodb.UpdateTimeToLiveOutput{}, nil
}

func activeTable() *types.TableDescription {
	return &types.TableDescription{TableStatus: types.TableStatusActive, KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}}, AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}}}
}
func notFound() error { return &types.ResourceNotFoundException{Message: aws.String("missing")} }

func TestSyncAllProvisioningFailuresAndNoop(t *testing.T) {
	want := Table{Name: "t", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash}}
	if got, err := Sync(context.Background(), &scriptedAPI{}, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty sync %v %v", got, err)
	}
	if got, err := Sync(context.Background(), &scriptedAPI{describeErrs: []error{errors.New("offline")}}, []Table{want}); err == nil || len(got) != 0 {
		t.Fatalf("describe error %v %v", got, err)
	}
	if got, err := Sync(context.Background(), &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{}}}, []Table{want}); err == nil || len(got) != 0 {
		t.Fatalf("nil table response %v %v", got, err)
	}
	failedCreate := &scriptedAPI{describeErrs: []error{notFound()}, createErr: errors.New("denied")}
	if got, err := Sync(context.Background(), failedCreate, []Table{want}); err == nil || len(got) != 0 {
		t.Fatalf("create error %v %v", got, err)
	}
	if failedCreate.createInput == nil {
		t.Fatal("create was not attempted")
	}
	waitErr := &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{Table: activeTable()}}, describeErrs: []error{nil, errors.New("wait describe")}}
	if _, err := Sync(context.Background(), waitErr, []Table{want}); err == nil {
		t.Fatal("sync should return wait failure")
	}
	drift := &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{Table: activeTable()}}}
	created, err := Sync(context.Background(), drift, []Table{want, want})
	if err != nil || len(created) != 0 || drift.describeCalls != 3 {
		t.Fatalf("duplicate physical tables should be checked once: %v %v calls=%d", created, err, drift.describeCalls)
	}
}

func TestCreateBuildsPrimaryGSIAndLSIAndWaitErrors(t *testing.T) {
	table := Table{Name: "events", Partition: Key{Name: "tenant", Type: types.ScalarAttributeTypeS}, Sort: Key{Name: "id", Type: types.ScalarAttributeTypeN}, Indexes: []Index{
		{Name: "ByStatus", Partition: Key{Name: "status", Type: types.ScalarAttributeTypeS}, Sort: Key{Name: "created", Type: types.ScalarAttributeTypeN}},
		{Name: "LocalByTime", Local: true, Partition: Key{Name: "tenant", Type: types.ScalarAttributeTypeS}, Sort: Key{Name: "time", Type: types.ScalarAttributeTypeN}},
	}}
	f := &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{Table: activeTable()}}}
	if err := create(context.Background(), f, table); err != nil {
		t.Fatal(err)
	}
	if f.createInput == nil || len(f.createInput.GlobalSecondaryIndexes) != 1 || len(f.createInput.LocalSecondaryIndexes) != 1 || len(f.createInput.KeySchema) != 2 {
		t.Fatalf("unexpected create: %#v", f.createInput)
	}
	for i, d := range f.createInput.AttributeDefinitions {
		if i > 0 && *f.createInput.AttributeDefinitions[i-1].AttributeName > *d.AttributeName {
			t.Fatalf("attribute definitions not sorted: %#v", f.createInput.AttributeDefinitions)
		}
	}
	if f.createInput.BillingMode != types.BillingModePayPerRequest {
		t.Fatal(f.createInput.BillingMode)
	}
	createFailure := &scriptedAPI{createErr: errors.New("create failed")}
	if err := create(context.Background(), createFailure, table); err == nil {
		t.Fatal("create should return error")
	}
	waitFailure := &scriptedAPI{describeErrs: []error{errors.New("describe after create")}}
	if err := create(context.Background(), waitFailure, table); err == nil {
		t.Fatal("create should return wait error")
	}
}

func TestWaitActiveStatesAndCancellation(t *testing.T) {
	active := activeTable()
	if err := waitActiveWith(context.Background(), &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{Table: active}}}, "t", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := waitActiveWith(context.Background(), &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{}}}, "t", time.Second, time.Millisecond); err == nil {
		t.Fatal("nil table should time out")
	}
	if err := waitActiveWith(context.Background(), &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{Table: active}}, describeErrs: []error{errors.New("describe")}}, "t", time.Second, time.Millisecond); err == nil {
		t.Fatal("describe error")
	}
	nonActive := *active
	nonActive.TableStatus = types.TableStatusCreating
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitActiveWith(ctx, &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{Table: &nonActive}}}, "t", time.Second, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result: %v", err)
	}
	if err := waitActiveWith(context.Background(), &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{Table: &nonActive}}}, "t", time.Millisecond, time.Hour); err == nil {
		t.Fatal("expected timeout")
	}
	inactiveIndex := *active
	inactiveIndex.GlobalSecondaryIndexes = []types.GlobalSecondaryIndexDescription{{IndexStatus: types.IndexStatusCreating}}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := waitActiveWith(ctx, &scriptedAPI{describes: []*dynamodb.DescribeTableOutput{{Table: &inactiveIndex}}}, "t", time.Second, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("inactive index result: %v", err)
	}
}

func TestDriftReportsEveryResourceDifference(t *testing.T) {
	want := Table{Name: "all", Partition: Key{Name: "pk", Type: types.ScalarAttributeTypeS}, Sort: Key{Name: "sk", Type: types.ScalarAttributeTypeN}, Indexes: []Index{
		{Name: "gsi", Partition: Key{Name: "gpk", Type: types.ScalarAttributeTypeS}, Sort: Key{Name: "gsk", Type: types.ScalarAttributeTypeN}},
		{Name: "lsi", Local: true, Partition: Key{Name: "pk", Type: types.ScalarAttributeTypeS}, Sort: Key{Name: "lsk", Type: types.ScalarAttributeTypeS}},
	}}
	got := &types.TableDescription{
		KeySchema:              []types.KeySchemaElement{{AttributeName: aws.String("wrong"), KeyType: types.KeyTypeRange}},
		AttributeDefinitions:   []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeN}},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndexDescription{{IndexName: aws.String("gsi"), KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("bad"), KeyType: types.KeyTypeRange}}, Projection: &types.Projection{ProjectionType: types.ProjectionTypeKeysOnly}}, {IndexName: aws.String("extra")}},
		LocalSecondaryIndexes:  []types.LocalSecondaryIndexDescription{{IndexName: aws.String("lsi"), KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeRange}}, Projection: nil}},
	}
	gotDrift := drift(want, got)
	joined := strings.Join(gotDrift, "|")
	for _, part := range []string{"primary key schema differs", "attribute type differs for pk", "attribute type differs for sk", "attribute type differs for gpk", "attribute type differs for gsk", "attribute type differs for lsk", "index gsi key schema differs", "index gsi must project all attributes", "index lsi key schema differs", "index lsi must project all attributes", "undeclared indexes exist"} {
		if !strings.Contains(joined, part) {
			t.Errorf("missing drift %q in %v", part, gotDrift)
		}
	}
	missing := drift(want, &types.TableDescription{})
	if !strings.Contains(strings.Join(missing, "|"), "missing GSI gsi") || !strings.Contains(strings.Join(missing, "|"), "missing LSI lsi") {
		t.Fatalf("missing indexes: %v", missing)
	}
	allProjection := drift(Table{Name: "t", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}}, activeTable())
	if len(allProjection) != 0 {
		t.Fatalf("matching table drift: %v", allProjection)
	}
}
