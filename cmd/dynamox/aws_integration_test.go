package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/backup"
	"github.com/liberocks/dynamox/examples/blog/dynamoxgen"
	"github.com/liberocks/dynamox/examples/transaction"
	"github.com/liberocks/dynamox/examples/transaction/source"
	"github.com/liberocks/dynamox/examples/transaction/target"
	"github.com/liberocks/dynamox/runtime"
)

// TestAWSProfileEndToEnd is opt-in. Set DYNAMOX_AWS_PROFILE to a profile that
// can manage isolated DynamoDB tables, PITR, and on-demand backups.
func TestAWSProfileEndToEnd(t *testing.T) {
	profile := os.Getenv("DYNAMOX_AWS_PROFILE")
	if profile == "" {
		t.Skip("set DYNAMOX_AWS_PROFILE to run the AWS integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(profile))
	if err != nil {
		t.Fatal("load AWS test profile:", err)
	}
	if cfg.Region == "" {
		t.Fatal("AWS test profile must have a default region")
	}
	db := dynamodb.NewFromConfig(cfg)
	prefix := fmt.Sprintf("dynamox_it_%x", time.Now().UnixNano())
	tables := []string{prefix + "_users", prefix + "_posts", prefix + "_sequence_items", prefix + "_serial_counter", prefix + "_source", prefix + "_target"}
	backupSuffix := "backup-" + fmt.Sprintf("%x", time.Now().UnixNano())
	pitrSuffix := "pitr-" + fmt.Sprintf("%x", time.Now().UnixNano())
	allNames := append([]string{}, tables...)
	for _, suffix := range []string{backupSuffix, pitrSuffix} {
		for _, sourceName := range tables {
			allNames = append(allNames, sourceName+"-"+suffix)
		}
	}
	for _, name := range allNames {
		_, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
		if err == nil {
			t.Fatalf("isolated test table name already exists: %s", name)
		}
		var missing *types.ResourceNotFoundException
		if !errors.As(err, &missing) {
			t.Fatalf("preflight isolated test table: %v", err)
		}
	}
	if err := preflightBackupNames(ctx, db, prefix); err != nil {
		t.Fatal("preflight isolated backup names:", err)
	}
	work := t.TempDir()
	schemaPath := filepath.Join(work, "schema.dynamox")
	manifestPath := filepath.Join(work, "backup.json")
	backupRestoreState := filepath.Join(work, "backup-restore.json")
	pitrRestoreState := filepath.Join(work, "pitr-restore.json")
	cleaned := false
	t.Cleanup(func() {
		if cleaned {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cleanupCancel()
		if err := cleanupAWSResources(cleanupCtx, db, allNames, manifestPath, prefix); err != nil {
			t.Errorf("clean up isolated AWS resources: %v", err)
		}
	})
	schemaSource := fmt.Sprintf(`model User {
  id String @id
  name String
  @@table(name: %q)
  @@pitr(enabled: true, recoveryDays: 1)
}
model Post {
  id String @id
  authorId String
  createdAt DateTime
  expiresAt Int?
  @@table(name: %q)
  @@index(name: "ByAuthor", partition: authorId, sort: createdAt, type: GSI)
  @@ttl(field: expiresAt)
  @@pitr(enabled: true, recoveryDays: 1)
}
model SequenceItem {
  id Int @id
  value String
  @@table(name: %q)
  @@pitr(enabled: true, recoveryDays: 1)
}
model SerialCounter {
  name String @id
  @@table(name: %q)
  @@pitr(enabled: true, recoveryDays: 1)
}
model SourceRecord {
  PK String @partitionKey
  SK String @sortKey
  status String
  updatedAt DateTime
  @@table(name: %q)
  @@pitr(enabled: true, recoveryDays: 1)
}
model TargetRecord {
  PK String @partitionKey
  SK String @sortKey
  status String
  @@table(name: %q)
  @@pitr(enabled: true, recoveryDays: 1)
}`, tables[0], tables[1], tables[2], tables[3], tables[4], tables[5])
	if err := os.WriteFile(schemaPath, []byte(schemaSource), 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitSync(ctx, profile, schemaPath); err != nil {
		t.Fatal("sync isolated schema and wait for TTL/PITR configuration:", err)
	}

	tableOverrides := map[string]string{
		"users": tables[0], "posts": tables[1], "sequence_items": tables[2], runtime.SequenceTableName: tables[3],
	}
	client := dynamoxgen.NewClientWithOptions(db, runtime.ClientOptions{TableNames: tableOverrides})
	preferences := dynamoxgen.Preferences{Theme: "dark", Digest: true}
	user, err := client.CreateUser(ctx, dynamoxgen.User{
		Name: "Integration User", Preferences: preferences, Settings: preferences,
		DocumentPreferences: dynamoxgen.DocumentMetadata{Source: "aws-test", Provider: runtime.JSON(map[string]any{"ok": true})},
		ProviderPayload:     runtime.JSON(map[string]any{"enabled": true}), Payloads: []runtime.JSON{},
	})
	if err != nil {
		t.Fatal("generated client create:", err)
	}
	gotUser, found, err := client.GetUserWithOptions(ctx, dynamoxgen.UserKey{ID: user.ID}, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || gotUser.Name != user.Name {
		t.Fatalf("generated client consistent get failed: found=%v err=%v", found, err)
	}
	post, err := client.CreatePost(ctx, dynamoxgen.Post{AuthorID: user.ID, Title: "Backup integration", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal("generated client create post:", err)
	}
	queried, err := client.QueryPostUsingByAuthor(ctx, user.ID, nil, runtime.PageRequest{Limit: 5})
	if err != nil || len(queried.Items) != 1 || queried.Items[0].ID != post.ID {
		t.Fatalf("generated GSI query failed: %#v err=%v", queried, err)
	}
	related, err := client.GetUserWithPosts(ctx, dynamoxgen.UserKey{ID: user.ID}, runtime.PageRequest{Limit: 5})
	if err != nil || len(related.Posts.Items) != 1 {
		t.Fatalf("generated relation read failed: %#v err=%v", related, err)
	}
	scanned, err := client.ScanUserWithOptions(ctx, runtime.ScanOptions{Page: runtime.PageRequest{Limit: 10, ConsistentRead: true}, Filter: &runtime.Expression{Text: "#name = :name", Names: map[string]string{"#name": "name"}, Values: map[string]any{":name": user.Name}}})
	if err != nil || len(scanned.Items) != 1 {
		t.Fatalf("generated filtered scan failed: %#v err=%v", scanned, err)
	}
	nickname := "Recovered"
	updated, err := client.UpdateUserReturning(ctx, dynamoxgen.UserKey{ID: user.ID}, dynamoxgen.UserUpdate{SetNickname: &nickname})
	if err != nil || updated.Nickname == nil || *updated.Nickname != nickname {
		t.Fatalf("generated ALL_NEW update failed: %#v err=%v", updated, err)
	}
	if err := client.PutUser(ctx, updated); err != nil {
		t.Fatal("generated put/upsert failed:", err)
	}
	putUser, found, err := client.GetUser(ctx, dynamoxgen.UserKey{ID: user.ID})
	if err != nil || !found || putUser.Name != updated.Name {
		t.Fatalf("generated put/upsert readback failed: found=%v err=%v", found, err)
	}
	deleteUser, err := client.CreateUser(ctx, dynamoxgen.User{Name: "Delete integration"})
	if err != nil {
		t.Fatal("generated delete fixture create:", err)
	}
	if err := client.DeleteUser(ctx, dynamoxgen.UserKey{ID: deleteUser.ID}); err != nil {
		t.Fatal("generated delete failed:", err)
	}
	if _, found, err := client.GetUser(ctx, dynamoxgen.UserKey{ID: deleteUser.ID}); err != nil || found {
		t.Fatalf("generated delete readback failed: found=%v err=%v", found, err)
	}
	serial, err := client.CreateSequenceItem(ctx, dynamoxgen.SequenceItem{Value: "counter before backup"})
	if err != nil || serial.ID < 1 {
		t.Fatalf("generated serial ID failed: %#v err=%v", serial, err)
	}
	if err := client.DeleteUserWithOptions(ctx, dynamoxgen.UserKey{ID: "missing"}, runtime.WriteOptions{Condition: &runtime.Expression{Text: "attribute_exists(#id)", Names: map[string]string{"#id": "id"}}}); err == nil {
		t.Fatal("conditional delete of a missing item should fail")
	}

	sourceClient := source.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"source-records": tables[4]}})
	targetClient := target.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"target-records": tables[5]}})
	key := source.SourceRecordKey{PK: "ENTITY#test", SK: "SOURCE#one"}
	if _, err := sourceClient.CreateSourceRecord(ctx, source.SourceRecord{PK: key.PK, SK: key.SK, Status: "pending", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal("seed transaction source:", err)
	}
	transactionTarget := target.TargetRecord{PK: key.PK, SK: "TARGET#one", Status: "processed", Details: []target.Detail{}, Payload: runtime.JSON(map[string]any{"test": true})}
	if err := transaction.CommitRecords(ctx, sourceClient, targetClient, key, transactionTarget); err != nil {
		t.Fatal("cross-client transaction:", err)
	}

	if err := quietRun(t, []string{"backup", "create", "--profile", profile, "--name", prefix, "--manifest", manifestPath, "--writes-paused", schemaPath}); err != nil {
		t.Fatal("create on-demand backup set:", err)
	}
	if err := quietRun(t, []string{"backup", "list", "--profile", profile, schemaPath}); err != nil {
		t.Fatal("list on-demand backups:", err)
	}
	if err := quietRun(t, []string{"backup", "status", "--profile", profile, "--manifest", manifestPath}); err != nil {
		t.Fatal("inspect backup set:", err)
	}
	if err := quietRun(t, []string{"restore", "backup", "--profile", profile, "--manifest", manifestPath, "--state", backupRestoreState, "--suffix", backupSuffix, "--writes-paused", schemaPath}); err != nil {
		if !strings.Contains(err.Error(), "TTL change is in progress") && !strings.Contains(err.Error(), "PITR change is in progress") {
			t.Fatal("restore backup set:", err)
		}
		if err := resumeUntilConfigured(t, profile, backupRestoreState, schemaPath); err != nil {
			t.Fatal("finish backup restore:", err)
		}
	}
	backupState, err := backup.Load(backupRestoreState)
	if err != nil {
		t.Fatal("read backup restore state:", err)
	}
	recoveredOverrides := make(map[string]string)
	for _, table := range backupState.Tables {
		recoveredOverrides[table.SourceTable] = table.TargetTable
	}
	recoveredClient := dynamoxgen.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{
		"users": recoveredOverrides[tables[0]], "posts": recoveredOverrides[tables[1]], "sequence_items": recoveredOverrides[tables[2]], runtime.SequenceTableName: recoveredOverrides[tables[3]],
	}})
	recoveredUser, found, err := recoveredClient.GetUser(ctx, dynamoxgen.UserKey{ID: user.ID})
	if err != nil || !found || recoveredUser.Name != user.Name {
		t.Fatalf("restored generated client mapping failed: found=%v err=%v", found, err)
	}
	recoveredSerial, err := recoveredClient.CreateSequenceItem(ctx, dynamoxgen.SequenceItem{Value: "counter after backup restore"})
	if err != nil || recoveredSerial.ID <= serial.ID {
		t.Fatalf("restored serial counter did not advance: before=%d after=%d err=%v", serial.ID, recoveredSerial.ID, err)
	}
	recoveredSource := source.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"source-records": recoveredOverrides[tables[4]]}})
	recoveredTarget := target.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{"target-records": recoveredOverrides[tables[5]]}})
	restoredSource, found, err := recoveredSource.GetSourceRecordWithOptions(ctx, key, runtime.ReadOptions{ConsistentRead: true})
	if err != nil || !found || restoredSource.Status != "processed" {
		t.Fatalf("restored transaction source missing: found=%v err=%v", found, err)
	}
	if _, found, err := recoveredTarget.GetTargetRecordWithOptions(ctx, target.TargetRecordKey{PK: transactionTarget.PK, SK: transactionTarget.SK}, runtime.ReadOptions{ConsistentRead: true}); err != nil || !found {
		t.Fatalf("restored transaction target missing: found=%v err=%v", found, err)
	}

	lastWrite := time.Now().UTC()
	at, err := waitRestorableTime(ctx, db, tables, lastWrite)
	if err != nil {
		t.Fatal("wait for a common PITR recovery point:", err)
	}
	if err := quietRun(t, []string{"restore", "pitr", "--profile", profile, "--at", at.Format(time.RFC3339), "--state", pitrRestoreState, "--suffix", pitrSuffix, "--writes-paused", schemaPath}); err != nil {
		if !strings.Contains(err.Error(), "TTL change is in progress") && !strings.Contains(err.Error(), "PITR change is in progress") {
			t.Fatal("restore PITR set:", err)
		}
		if err := resumeUntilConfigured(t, profile, pitrRestoreState, schemaPath); err != nil {
			t.Fatal("finish PITR restore:", err)
		}
	}
	if err := quietRun(t, []string{"restore", "status", "--profile", profile, "--state", pitrRestoreState}); err != nil {
		t.Fatal("inspect PITR restore:", err)
	}
	if err := quietRun(t, []string{"restore", "resume", "--profile", profile, "--state", pitrRestoreState, "--writes-paused", schemaPath}); err != nil {
		t.Fatal("resume completed PITR restore:", err)
	}
	pitrManifest, err := backup.Load(pitrRestoreState)
	if err != nil {
		t.Fatal("read PITR restore mapping:", err)
	}
	pitrMappings := make(map[string]string)
	for _, table := range pitrManifest.Tables {
		pitrMappings[table.SourceTable] = table.TargetTable
	}
	pitrClient := dynamoxgen.NewClientWithOptions(db, runtime.ClientOptions{TableNames: map[string]string{
		"users": pitrMappings[tables[0]], "posts": pitrMappings[tables[1]], "sequence_items": pitrMappings[tables[2]], runtime.SequenceTableName: pitrMappings[tables[3]],
	}})
	pitrUser, found, err := pitrClient.GetUser(ctx, dynamoxgen.UserKey{ID: user.ID})
	if err != nil || !found || pitrUser.Name != user.Name {
		t.Fatalf("PITR restored generated client mapping failed: found=%v err=%v", found, err)
	}
	pitrSerial, err := pitrClient.CreateSequenceItem(ctx, dynamoxgen.SequenceItem{Value: "counter after PITR restore"})
	if err != nil || pitrSerial.ID <= serial.ID {
		t.Fatalf("PITR restored serial counter did not advance: before=%d after=%d err=%v", serial.ID, pitrSerial.ID, err)
	}
	if err := cleanupAWSResources(ctx, db, allNames, manifestPath, prefix); err != nil {
		t.Fatal("clean up isolated AWS resources:", err)
	}
	for _, name := range allNames {
		if _, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}); err == nil {
			t.Fatalf("test table remains after cleanup: %s", name)
		} else {
			var missing *types.ResourceNotFoundException
			if !errors.As(err, &missing) {
				t.Fatalf("verify test table cleanup: %v", err)
			}
		}
	}
	if err := verifyBackupsRemoved(ctx, db, prefix); err != nil {
		t.Fatal("verify test backup cleanup:", err)
	}
	cleaned = true
}

func quietRun(t *testing.T, args []string) error {
	if t != nil {
		t.Helper()
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stdout = w
	runErr := run(args)
	_ = w.Close()
	os.Stdout = old
	_, copyErr := io.Copy(io.Discard, r)
	_ = r.Close()
	if copyErr != nil {
		return copyErr
	}
	return runErr
}

func waitSync(ctx context.Context, profile, schemaPath string) error {
	deadline, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		err := quietRun(nil, []string{"sync", "--profile", profile, schemaPath})
		if err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "TTL change is in progress") && !strings.Contains(err.Error(), "PITR change is in progress") {
			return err
		}
		select {
		case <-deadline.Done():
			return deadline.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func resumeUntilConfigured(t *testing.T, profile, statePath, schemaPath string) error {
	t.Helper()
	deadline := time.Now().Add(15 * time.Minute)
	for {
		err := quietRun(t, []string{"restore", "resume", "--profile", profile, "--state", statePath, "--writes-paused", schemaPath})
		if err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "TTL change is in progress") && !strings.Contains(err.Error(), "PITR change is in progress") {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Second)
	}
}

func waitRestorableTime(ctx context.Context, db *dynamodb.Client, tables []string, after time.Time) (time.Time, error) {
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		var earliest, latest time.Time
		ready := true
		for _, name := range tables {
			out, err := db.DescribeContinuousBackups(ctx, &dynamodb.DescribeContinuousBackupsInput{TableName: aws.String(name)})
			if err != nil {
				return time.Time{}, err
			}
			if out == nil || out.ContinuousBackupsDescription == nil || out.ContinuousBackupsDescription.PointInTimeRecoveryDescription == nil {
				ready = false
				break
			}
			pitr := out.ContinuousBackupsDescription.PointInTimeRecoveryDescription
			if pitr.PointInTimeRecoveryStatus != types.PointInTimeRecoveryStatusEnabled || pitr.EarliestRestorableDateTime == nil || pitr.LatestRestorableDateTime == nil {
				ready = false
				break
			}
			if earliest.IsZero() || pitr.EarliestRestorableDateTime.After(earliest) {
				earliest = *pitr.EarliestRestorableDateTime
			}
			if latest.IsZero() || pitr.LatestRestorableDateTime.Before(latest) {
				latest = *pitr.LatestRestorableDateTime
			}
		}
		at := latest.Add(-30 * time.Second).UTC().Truncate(time.Second)
		if ready && !at.Before(earliest) && at.After(after) {
			return at, nil
		}
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
	return time.Time{}, fmt.Errorf("PITR did not expose a common restorable instant after the test writes")
}

func preflightBackupNames(ctx context.Context, db *dynamodb.Client, prefix string) error {
	backups, err := backupsWithPrefix(ctx, db, prefix)
	if err != nil {
		return fmt.Errorf("list existing backups: %w", err)
	}
	if len(backups) > 0 {
		return fmt.Errorf("isolated backup name already exists")
	}
	return nil
}

func cleanupAWSResources(ctx context.Context, db *dynamodb.Client, tables []string, manifestPath, backupPrefix string) error {
	var failures []string
	backupARNs := map[string]bool{}
	if manifest, err := backup.Load(manifestPath); err == nil {
		for _, entry := range manifest.Tables {
			if entry.BackupARN == "" {
				continue
			}
			backupARNs[entry.BackupARN] = true
		}
	}
	listed, err := backupsWithPrefix(ctx, db, backupPrefix)
	if err != nil {
		failures = append(failures, "list test backups: "+err.Error())
	} else {
		for _, arn := range listed {
			backupARNs[arn] = true
		}
	}
	for arn := range backupARNs {
		if err := deleteBackupAndWait(ctx, db, arn); err != nil {
			failures = append(failures, "delete or verify backup: "+err.Error())
		}
	}
	for _, name := range tables {
		if err := waitTableActiveOrMissing(ctx, db, name); err != nil {
			failures = append(failures, "wait for test table before cleanup: "+err.Error())
			continue
		}
		_, err := db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(name)})
		if err != nil {
			var missing *types.ResourceNotFoundException
			if !errors.As(err, &missing) {
				failures = append(failures, "delete table: "+err.Error())
				continue
			}
		}
		waiter := dynamodb.NewTableNotExistsWaiter(db)
		if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, 10*time.Minute); err != nil {
			failures = append(failures, "wait for table deletion: "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func waitTableActiveOrMissing(ctx context.Context, db *dynamodb.Client, name string) error {
	for {
		out, err := db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
		if err != nil {
			var missing *types.ResourceNotFoundException
			if errors.As(err, &missing) {
				return nil
			}
			return err
		}
		if out != nil && out.Table != nil && out.Table.TableStatus == types.TableStatusActive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func deleteBackupAndWait(ctx context.Context, db *dynamodb.Client, arn string) error {
	deadline := time.Now().Add(15 * time.Minute)
	deleteRequested := false
	for time.Now().Before(deadline) {
		out, err := db.DescribeBackup(ctx, &dynamodb.DescribeBackupInput{BackupArn: aws.String(arn)})
		if err != nil {
			var missing *types.BackupNotFoundException
			if errors.As(err, &missing) {
				return nil
			}
			return err
		}
		if out == nil || out.BackupDescription == nil || out.BackupDescription.BackupDetails == nil {
			return fmt.Errorf("backup description is missing")
		}
		status := out.BackupDescription.BackupDetails.BackupStatus
		if status == types.BackupStatusDeleted {
			return nil
		}
		if status == types.BackupStatusAvailable && !deleteRequested {
			if _, err := db.DeleteBackup(ctx, &dynamodb.DeleteBackupInput{BackupArn: aws.String(arn)}); err != nil {
				var missing *types.BackupNotFoundException
				if errors.As(err, &missing) {
					return nil
				}
				return err
			}
			deleteRequested = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("backup deletion did not finish before timeout")
}

func verifyBackupsRemoved(ctx context.Context, db *dynamodb.Client, prefix string) error {
	backups, err := backupsWithPrefix(ctx, db, prefix)
	if err != nil {
		return fmt.Errorf("list backups after cleanup: %w", err)
	}
	if len(backups) > 0 {
		return fmt.Errorf("test backup remains after cleanup")
	}
	return nil
}

func backupsWithPrefix(ctx context.Context, db *dynamodb.Client, prefix string) ([]string, error) {
	var result []string
	var cursor *string
	for {
		out, err := db.ListBackups(ctx, &dynamodb.ListBackupsInput{BackupType: types.BackupTypeFilterUser, ExclusiveStartBackupArn: cursor})
		if err != nil {
			return result, err
		}
		for _, item := range out.BackupSummaries {
			if strings.HasPrefix(aws.ToString(item.BackupName), prefix+"-") && item.BackupStatus != types.BackupStatusDeleted && aws.ToString(item.BackupArn) != "" {
				result = append(result, aws.ToString(item.BackupArn))
			}
		}
		cursor = out.LastEvaluatedBackupArn
		if cursor == nil || *cursor == "" {
			return result, nil
		}
	}
}
