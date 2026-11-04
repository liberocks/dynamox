package provision

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
)

type statefulAPI struct {
	table          *types.TableDescription
	missing        bool
	createInput    *dynamodb.CreateTableInput
	tableUpdates   []*dynamodb.UpdateTableInput
	ttl            *types.TimeToLiveDescription
	ttlDescribeNil bool
	ttlUpdates     []*dynamodb.UpdateTimeToLiveInput
	createErr      error
	tableUpdateErr error
	ttlDescribeErr error
	ttlUpdateErr   error
	describeErr    error
	describeErrAt  int
	describeNilAt  int
	describeCalls  int
}

func (f *statefulAPI) DescribeTable(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	f.describeCalls++
	if f.describeErr != nil && (f.describeErrAt == 0 || f.describeErrAt == f.describeCalls) {
		return nil, f.describeErr
	}
	if f.describeNilAt == f.describeCalls {
		return &dynamodb.DescribeTableOutput{}, nil
	}
	if f.missing {
		return nil, notFound()
	}
	return &dynamodb.DescribeTableOutput{Table: f.table}, nil
}

func (f *statefulAPI) CreateTable(_ context.Context, in *dynamodb.CreateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	f.createInput = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.missing = false
	f.table = tableDescriptionFromCreate(in)
	return &dynamodb.CreateTableOutput{}, nil
}

func (f *statefulAPI) UpdateTable(_ context.Context, in *dynamodb.UpdateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateTableOutput, error) {
	f.tableUpdates = append(f.tableUpdates, in)
	if f.tableUpdateErr != nil {
		return nil, f.tableUpdateErr
	}
	if in.BillingMode != "" {
		f.table.BillingModeSummary = &types.BillingModeSummary{BillingMode: in.BillingMode}
		if in.BillingMode == types.BillingModePayPerRequest {
			f.table.ProvisionedThroughput = nil
			for i := range f.table.GlobalSecondaryIndexes {
				f.table.GlobalSecondaryIndexes[i].ProvisionedThroughput = nil
			}
		}
	}
	if in.ProvisionedThroughput != nil {
		f.table.ProvisionedThroughput = throughputDescription(aws.ToInt64(in.ProvisionedThroughput.ReadCapacityUnits), aws.ToInt64(in.ProvisionedThroughput.WriteCapacityUnits))
	}
	for _, indexUpdate := range in.GlobalSecondaryIndexUpdates {
		if indexUpdate.Update == nil {
			continue
		}
		for i := range f.table.GlobalSecondaryIndexes {
			index := &f.table.GlobalSecondaryIndexes[i]
			if aws.ToString(index.IndexName) == aws.ToString(indexUpdate.Update.IndexName) {
				index.ProvisionedThroughput = throughputDescription(aws.ToInt64(indexUpdate.Update.ProvisionedThroughput.ReadCapacityUnits), aws.ToInt64(indexUpdate.Update.ProvisionedThroughput.WriteCapacityUnits))
			}
		}
	}
	return &dynamodb.UpdateTableOutput{}, nil
}

func (f *statefulAPI) DescribeTimeToLive(context.Context, *dynamodb.DescribeTimeToLiveInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTimeToLiveOutput, error) {
	if f.ttlDescribeErr != nil {
		return nil, f.ttlDescribeErr
	}
	if f.ttlDescribeNil {
		return nil, nil
	}
	return &dynamodb.DescribeTimeToLiveOutput{TimeToLiveDescription: f.ttl}, nil
}

func (f *statefulAPI) UpdateTimeToLive(_ context.Context, in *dynamodb.UpdateTimeToLiveInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateTimeToLiveOutput, error) {
	f.ttlUpdates = append(f.ttlUpdates, in)
	if f.ttlUpdateErr != nil {
		return nil, f.ttlUpdateErr
	}
	status := types.TimeToLiveStatusDisabling
	if aws.ToBool(in.TimeToLiveSpecification.Enabled) {
		status = types.TimeToLiveStatusEnabling
	}
	f.ttl = &types.TimeToLiveDescription{AttributeName: in.TimeToLiveSpecification.AttributeName, TimeToLiveStatus: status}
	return &dynamodb.UpdateTimeToLiveOutput{}, nil
}

func tableDescriptionFromCreate(in *dynamodb.CreateTableInput) *types.TableDescription {
	table := &types.TableDescription{
		TableName: aws.String(aws.ToString(in.TableName)), TableStatus: types.TableStatusActive,
		KeySchema: in.KeySchema, AttributeDefinitions: in.AttributeDefinitions,
		BillingModeSummary: &types.BillingModeSummary{BillingMode: in.BillingMode},
	}
	if in.ProvisionedThroughput != nil {
		table.ProvisionedThroughput = throughputDescription(aws.ToInt64(in.ProvisionedThroughput.ReadCapacityUnits), aws.ToInt64(in.ProvisionedThroughput.WriteCapacityUnits))
	}
	for _, index := range in.GlobalSecondaryIndexes {
		description := types.GlobalSecondaryIndexDescription{IndexName: index.IndexName, KeySchema: index.KeySchema, Projection: index.Projection, IndexStatus: types.IndexStatusActive}
		if index.ProvisionedThroughput != nil {
			description.ProvisionedThroughput = throughputDescription(aws.ToInt64(index.ProvisionedThroughput.ReadCapacityUnits), aws.ToInt64(index.ProvisionedThroughput.WriteCapacityUnits))
		}
		table.GlobalSecondaryIndexes = append(table.GlobalSecondaryIndexes, description)
	}
	for _, index := range in.LocalSecondaryIndexes {
		table.LocalSecondaryIndexes = append(table.LocalSecondaryIndexes, types.LocalSecondaryIndexDescription{IndexName: index.IndexName, KeySchema: index.KeySchema, Projection: index.Projection})
	}
	return table
}

func throughputDescription(read, write int64) *types.ProvisionedThroughputDescription {
	return &types.ProvisionedThroughputDescription{ReadCapacityUnits: aws.Int64(read), WriteCapacityUnits: aws.Int64(write)}
}

func matchingProvisionedTable() *types.TableDescription {
	return tableDescriptionFromCreate(&dynamodb.CreateTableInput{
		TableName: aws.String("items"), BillingMode: types.BillingModeProvisioned,
		KeySchema:              []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		AttributeDefinitions:   []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("group"), AttributeType: types.ScalarAttributeTypeS}},
		ProvisionedThroughput:  provisionedThroughput(1, 1),
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{IndexName: aws.String("ByGroup"), KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("group"), KeyType: types.KeyTypeHash}}, Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll}, ProvisionedThroughput: provisionedThroughput(1, 1)}},
	})
}

func TestCreateProvisionedTableWithTTL(t *testing.T) {
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 4, WriteCapacityUnits: 2, TTL: "expiresAt", Indexes: []Index{{Name: "ByGroup", Partition: Key{Name: "group", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 2, WriteCapacityUnits: 1}}}
	db := &statefulAPI{missing: true, ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	created, err := Sync(context.Background(), db, []Table{want})
	if err == nil || !strings.Contains(err.Error(), "TTL change is in progress") {
		t.Fatalf("expected accepted TTL transition to be reported, created=%v err=%v", created, err)
	}
	if len(created) != 1 || created[0] != want.Name {
		t.Fatalf("created table list: %v", created)
	}
	if db.createInput == nil || db.createInput.BillingMode != types.BillingModeProvisioned || aws.ToInt64(db.createInput.ProvisionedThroughput.ReadCapacityUnits) != 4 || aws.ToInt64(db.createInput.ProvisionedThroughput.WriteCapacityUnits) != 2 {
		t.Fatalf("created provisioned throughput: %#v", db.createInput)
	}
	if len(db.createInput.GlobalSecondaryIndexes) != 1 || aws.ToInt64(db.createInput.GlobalSecondaryIndexes[0].ProvisionedThroughput.ReadCapacityUnits) != 2 || aws.ToInt64(db.createInput.GlobalSecondaryIndexes[0].ProvisionedThroughput.WriteCapacityUnits) != 1 {
		t.Fatalf("created GSI throughput: %#v", db.createInput.GlobalSecondaryIndexes)
	}
	if len(db.ttlUpdates) != 1 || !aws.ToBool(db.ttlUpdates[0].TimeToLiveSpecification.Enabled) || aws.ToString(db.ttlUpdates[0].TimeToLiveSpecification.AttributeName) != want.TTL {
		t.Fatalf("TTL enable request: %#v", db.ttlUpdates)
	}
}

func TestSyncReconcilesThroughputAndTTLTransitionsWithoutRepeating(t *testing.T) {
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 3, WriteCapacityUnits: 2, TTL: "expiresAt", Indexes: []Index{{Name: "ByGroup", Partition: Key{Name: "group", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 2, WriteCapacityUnits: 1}}}
	db := &statefulAPI{table: matchingProvisionedTable(), ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	if _, err := Sync(context.Background(), db, []Table{want}); err == nil || !strings.Contains(err.Error(), "TTL change is in progress") {
		t.Fatalf("first sync should report pending TTL: %v", err)
	}
	if len(db.tableUpdates) != 2 || db.tableUpdates[0].ProvisionedThroughput == nil || len(db.tableUpdates[1].GlobalSecondaryIndexUpdates) != 1 {
		t.Fatalf("table/GSI updates: %#v", db.tableUpdates)
	}
	if _, err := Sync(context.Background(), db, []Table{want}); err == nil || len(db.tableUpdates) != 2 || len(db.ttlUpdates) != 1 {
		t.Fatalf("in-progress TTL should not trigger repeated updates, err=%v tableUpdates=%d ttlUpdates=%d", err, len(db.tableUpdates), len(db.ttlUpdates))
	}
	db.ttl = &types.TimeToLiveDescription{AttributeName: aws.String(want.TTL), TimeToLiveStatus: types.TimeToLiveStatusEnabled}
	if _, err := Sync(context.Background(), db, []Table{want}); err != nil {
		t.Fatal(err)
	}
	want.TTL = "expiresAtV2"
	if _, err := Sync(context.Background(), db, []Table{want}); err == nil || len(db.ttlUpdates) != 2 || aws.ToBool(db.ttlUpdates[1].TimeToLiveSpecification.Enabled) {
		t.Fatalf("changing the TTL attribute should begin by disabling the old attribute: err=%v requests=%#v", err, db.ttlUpdates)
	}
	if _, err := Sync(context.Background(), db, []Table{want}); err == nil || len(db.ttlUpdates) != 2 {
		t.Fatalf("disabling state should not issue another request: err=%v requests=%d", err, len(db.ttlUpdates))
	}
	db.ttl = &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}
	if _, err := Sync(context.Background(), db, []Table{want}); err == nil || len(db.ttlUpdates) != 3 || !aws.ToBool(db.ttlUpdates[2].TimeToLiveSpecification.Enabled) {
		t.Fatalf("new TTL should enable after disable completes: err=%v requests=%#v", err, db.ttlUpdates)
	}
	db.ttl = &types.TimeToLiveDescription{AttributeName: aws.String(want.TTL), TimeToLiveStatus: types.TimeToLiveStatusEnabled}
	if _, err := Sync(context.Background(), db, []Table{want}); err != nil {
		t.Fatal(err)
	}
	want.TTL = ""
	if _, err := Sync(context.Background(), db, []Table{want}); err == nil || len(db.ttlUpdates) != 4 || aws.ToBool(db.ttlUpdates[3].TimeToLiveSpecification.Enabled) {
		t.Fatalf("omitted TTL should disable the current attribute: err=%v requests=%#v", err, db.ttlUpdates)
	}
	db.ttl = &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}
	if _, err := Sync(context.Background(), db, []Table{want}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncSwitchesProvisionedTableToOnDemand(t *testing.T) {
	db := &statefulAPI{table: matchingProvisionedTable(), ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, Indexes: []Index{{Name: "ByGroup", Partition: Key{Name: "group", Type: types.ScalarAttributeTypeS}}}}
	if _, err := Sync(context.Background(), db, []Table{want}); err != nil {
		t.Fatal(err)
	}
	if len(db.tableUpdates) != 1 || db.tableUpdates[0].BillingMode != types.BillingModePayPerRequest || db.tableUpdates[0].ProvisionedThroughput != nil {
		t.Fatalf("on-demand reconciliation: %#v", db.tableUpdates)
	}
}

func TestSyncSwitchesOnDemandTableWithGSIToProvisioned(t *testing.T) {
	got := matchingProvisionedTable()
	got.BillingModeSummary = &types.BillingModeSummary{BillingMode: types.BillingModePayPerRequest}
	got.ProvisionedThroughput = nil
	got.GlobalSecondaryIndexes[0].ProvisionedThroughput = nil
	db := &statefulAPI{table: got, ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	want := Table{
		Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 4, WriteCapacityUnits: 2,
		Indexes: []Index{{Name: "ByGroup", Partition: Key{Name: "group", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 3, WriteCapacityUnits: 1}},
	}
	if _, err := Sync(context.Background(), db, []Table{want}); err != nil {
		t.Fatal(err)
	}
	if len(db.tableUpdates) != 1 {
		t.Fatalf("expected one atomic capacity-mode update, got %#v", db.tableUpdates)
	}
	update := db.tableUpdates[0]
	if update.BillingMode != types.BillingModeProvisioned || update.ProvisionedThroughput == nil || aws.ToInt64(update.ProvisionedThroughput.ReadCapacityUnits) != 4 || aws.ToInt64(update.ProvisionedThroughput.WriteCapacityUnits) != 2 {
		t.Fatalf("table provisioned-mode update: %#v", update)
	}
	if len(update.GlobalSecondaryIndexUpdates) != 1 || update.GlobalSecondaryIndexUpdates[0].Update == nil || aws.ToString(update.GlobalSecondaryIndexUpdates[0].Update.IndexName) != "ByGroup" || aws.ToInt64(update.GlobalSecondaryIndexUpdates[0].Update.ProvisionedThroughput.ReadCapacityUnits) != 3 || aws.ToInt64(update.GlobalSecondaryIndexUpdates[0].Update.ProvisionedThroughput.WriteCapacityUnits) != 1 {
		t.Fatalf("GSI provisioned-mode update: %#v", update.GlobalSecondaryIndexUpdates)
	}
}

func TestSyncUpdateAndRefreshFailures(t *testing.T) {
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 3, WriteCapacityUnits: 2, Indexes: []Index{{Name: "ByGroup", Partition: Key{Name: "group", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 1, WriteCapacityUnits: 1}}}
	createTTLFailure := &statefulAPI{missing: true, ttlDescribeErr: errors.New("TTL denied")}
	created, err := Sync(context.Background(), createTTLFailure, []Table{{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, TTL: "expiresAt"}})
	if err == nil || len(created) != 1 || created[0] != "items" {
		t.Fatalf("created table should be reported when TTL setup fails: created=%v err=%v", created, err)
	}
	failedUpdate := &statefulAPI{table: matchingProvisionedTable(), tableUpdateErr: errors.New("denied"), ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	if _, err := Sync(context.Background(), failedUpdate, []Table{want}); err == nil || !strings.Contains(err.Error(), "update table items capacity") {
		t.Fatalf("expected table capacity update error: %v", err)
	}
	refreshFailure := &statefulAPI{table: matchingProvisionedTable(), describeErr: errors.New("offline"), describeErrAt: 5, ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	if _, err := Sync(context.Background(), refreshFailure, []Table{want}); err == nil || !strings.Contains(err.Error(), "describe table items") {
		t.Fatalf("expected post-update refresh error: %v", err)
	}
	refreshEmpty := &statefulAPI{table: matchingProvisionedTable(), describeNilAt: 5, ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	if _, err := Sync(context.Background(), refreshEmpty, []Table{want}); err == nil || !strings.Contains(err.Error(), "returned no description") {
		t.Fatalf("expected post-update empty description error: %v", err)
	}
	beforeReconcile := &statefulAPI{table: matchingProvisionedTable(), describeErr: errors.New("offline"), describeErrAt: 3, ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	if _, err := Sync(context.Background(), beforeReconcile, []Table{{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}}}); err == nil {
		t.Fatal("expected active table refresh error")
	}
	ttlFailure := &statefulAPI{table: activeTable(), ttlDescribeErr: errors.New("TTL denied")}
	if _, err := Sync(context.Background(), ttlFailure, []Table{{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}}}); err == nil || !strings.Contains(err.Error(), "describe table items TTL") {
		t.Fatalf("expected sync TTL error: %v", err)
	}
	tableWaitFailure := &statefulAPI{table: matchingProvisionedTable(), describeErr: errors.New("wait failed"), describeErrAt: 4}
	if _, err := Sync(context.Background(), tableWaitFailure, []Table{{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 3, WriteCapacityUnits: 2, Indexes: []Index{{Name: "ByGroup", Partition: Key{Name: "group", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 1, WriteCapacityUnits: 1}}}}); err == nil || !strings.Contains(err.Error(), "wait for table") {
		t.Fatalf("expected table capacity wait error: %v", err)
	}
	gsiWaitFailure := &statefulAPI{table: matchingProvisionedTable(), describeErr: errors.New("wait failed"), describeErrAt: 1}
	if err := reconcileCapacity(context.Background(), gsiWaitFailure, Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 1, WriteCapacityUnits: 1, Indexes: []Index{{Name: "ByGroup", ReadCapacityUnits: 2, WriteCapacityUnits: 1}}}, matchingProvisionedTable()); err == nil || !strings.Contains(err.Error(), "wait for table") {
		t.Fatalf("expected GSI capacity wait error: %v", err)
	}
	gsiRefreshFailure := &statefulAPI{table: matchingProvisionedTable(), describeErr: errors.New("refresh failed"), describeErrAt: 2}
	if err := reconcileCapacity(context.Background(), gsiRefreshFailure, Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 1, WriteCapacityUnits: 1, Indexes: []Index{{Name: "ByGroup", ReadCapacityUnits: 2, WriteCapacityUnits: 1}}}, matchingProvisionedTable()); err == nil || !strings.Contains(err.Error(), "describe table items") {
		t.Fatalf("expected GSI post-update refresh error: %v", err)
	}
	local := matchingProvisionedTable()
	local.LocalSecondaryIndexes = []types.LocalSecondaryIndexDescription{{IndexName: aws.String("ByLocal")}}
	if err := reconcileCapacity(context.Background(), &statefulAPI{table: local}, Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 1, WriteCapacityUnits: 1, Indexes: []Index{{Name: "ByLocal", Local: true}}}, local); err != nil {
		t.Fatalf("LSI should inherit table capacity: %v", err)
	}
}

func TestReconcileGSIUpdateErrorsAndBillingFallback(t *testing.T) {
	want := Table{Name: "items", Partition: Key{Name: "id", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 1, WriteCapacityUnits: 1, Indexes: []Index{{Name: "ByGroup", Partition: Key{Name: "group", Type: types.ScalarAttributeTypeS}, ReadCapacityUnits: 2, WriteCapacityUnits: 1}}}
	db := &statefulAPI{table: matchingProvisionedTable(), tableUpdateErr: errors.New("GSI denied")}
	db.table.ProvisionedThroughput = throughputDescription(1, 1)
	db.table.GlobalSecondaryIndexes[0].ProvisionedThroughput = throughputDescription(1, 1)
	if err := reconcileCapacity(context.Background(), db, want, db.table); err == nil || !strings.Contains(err.Error(), "GSI ByGroup capacity") {
		t.Fatalf("expected GSI update error: %v", err)
	}
	missingIndex := matchingProvisionedTable()
	missingIndex.GlobalSecondaryIndexes = nil
	wantTable := want
	wantTable.ReadCapacityUnits = 1
	wantTable.WriteCapacityUnits = 1
	if err := reconcileCapacity(context.Background(), &statefulAPI{}, wantTable, missingIndex); err == nil || !strings.Contains(err.Error(), "missing GSI") {
		t.Fatalf("expected missing GSI error: %v", err)
	}
	legacy := matchingProvisionedTable()
	legacy.BillingModeSummary = nil
	if mode := tableBillingMode(legacy); mode != types.BillingModeProvisioned {
		t.Fatalf("legacy billing mode: %s", mode)
	}
	if mode := tableBillingMode(activeTable()); mode != types.BillingModePayPerRequest {
		t.Fatalf("on-demand billing fallback: %s", mode)
	}
}

func TestReconcileTTLErrorsStatusesAndNoOp(t *testing.T) {
	want := Table{Name: "items", TTL: "expiresAt"}
	if _, err := reconcileTTL(context.Background(), &statefulAPI{ttlDescribeErr: errors.New("denied")}, want); err == nil || !strings.Contains(err.Error(), "describe table items TTL") {
		t.Fatalf("expected TTL describe error: %v", err)
	}
	if _, err := reconcileTTL(context.Background(), &statefulAPI{ttlDescribeNil: true}, want); err == nil || !strings.Contains(err.Error(), "returned no description") {
		t.Fatalf("expected empty TTL description error: %v", err)
	}
	db := &statefulAPI{ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}, ttlUpdateErr: errors.New("denied")}
	if _, err := reconcileTTL(context.Background(), db, want); err == nil || !strings.Contains(err.Error(), "update table items TTL") {
		t.Fatalf("expected TTL update error: %v", err)
	}
	for _, status := range []types.TimeToLiveStatus{types.TimeToLiveStatusEnabling, types.TimeToLiveStatusDisabling} {
		db := &statefulAPI{ttl: &types.TimeToLiveDescription{AttributeName: aws.String(want.TTL), TimeToLiveStatus: status}}
		pending, err := reconcileTTL(context.Background(), db, want)
		if err != nil || !pending || len(db.ttlUpdates) != 0 {
			t.Errorf("status %s: pending=%v err=%v updates=%d", status, pending, err, len(db.ttlUpdates))
		}
	}
	db = &statefulAPI{ttl: &types.TimeToLiveDescription{AttributeName: aws.String(want.TTL), TimeToLiveStatus: types.TimeToLiveStatusEnabled}}
	if pending, err := reconcileTTL(context.Background(), db, want); err != nil || pending {
		t.Fatalf("matching TTL should be a no-op: pending=%v err=%v", pending, err)
	}
	db = &statefulAPI{ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatus("FAILED")}}
	if _, err := reconcileTTL(context.Background(), db, want); err == nil || !strings.Contains(err.Error(), "unsupported TTL status") {
		t.Fatalf("unknown TTL status: %v", err)
	}
	db = &statefulAPI{ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusEnabled}}
	if _, err := reconcileTTL(context.Background(), db, Table{Name: "items"}); err == nil || !strings.Contains(err.Error(), "without an attribute") {
		t.Fatalf("enabled TTL without attribute: %v", err)
	}
	db = &statefulAPI{ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusDisabled}}
	if pending, err := reconcileTTL(context.Background(), db, Table{Name: "items"}); err != nil || pending || len(db.ttlUpdates) != 0 {
		t.Fatalf("disabled TTL should be a no-op: pending=%v err=%v", pending, err)
	}
	db = &statefulAPI{ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusEnabled, AttributeName: aws.String("oldExpiresAt")}, ttlUpdateErr: errors.New("disable denied")}
	if _, err := reconcileTTL(context.Background(), db, want); err == nil || !strings.Contains(err.Error(), "update table items TTL") {
		t.Fatalf("expected TTL disable error: %v", err)
	}
	db = &statefulAPI{ttl: &types.TimeToLiveDescription{TimeToLiveStatus: types.TimeToLiveStatusEnabled, AttributeName: aws.String("oldExpiresAt")}, ttlUpdateErr: &smithy.GenericAPIError{Code: "ValidationException", Message: "Time to live has been modified multiple times within a fixed interval"}}
	if _, err := reconcileTTL(context.Background(), db, want); err == nil || !strings.Contains(err.Error(), "one TTL configuration update per hour") {
		t.Fatalf("expected DynamoDB TTL rate limit guidance: %v", err)
	}
	db = &statefulAPI{ttl: &types.TimeToLiveDescription{}}
	if pending, err := reconcileTTL(context.Background(), db, want); err != nil || !pending || len(db.ttlUpdates) != 1 {
		t.Fatalf("empty status should be treated as disabled: pending=%v err=%v", pending, err)
	}
}
