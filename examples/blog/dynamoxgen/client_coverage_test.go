package dynamoxgen

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/runtime"
)

type generatedDynamo struct {
	runtime.DynamoAPI
	records                                                                           map[string]map[string]types.AttributeValue
	getErrors                                                                         map[string]error
	putErr, updateErr, deleteErr, queryErr, scanErr, transactGetErr, transactWriteErr error
	queryItems, scanItems                                                             map[string][]map[string]types.AttributeValue
	lastQuery                                                                         *dynamodb.QueryInput
	lastScan                                                                          *dynamodb.ScanInput
	lastPut                                                                           *dynamodb.PutItemInput
	lastUpdate                                                                        *dynamodb.UpdateItemInput
	lastDelete                                                                        *dynamodb.DeleteItemInput
	lastRead                                                                          *dynamodb.TransactGetItemsInput
	lastWrite                                                                         *dynamodb.TransactWriteItemsInput
	serialValue                                                                       int64
}

func (d *generatedDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	table := aws.ToString(in.TableName)
	if err := d.getErrors[table]; err != nil {
		return nil, err
	}
	id := generatedKeyValue(in.Key["id"])
	item := d.records[table+"/"+id]
	return &dynamodb.GetItemOutput{Item: item}, nil
}
func (d *generatedDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	d.lastPut = in
	if d.putErr != nil {
		return nil, d.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}
func (d *generatedDynamo) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	d.lastUpdate = in
	if d.updateErr != nil {
		return nil, d.updateErr
	}
	if aws.ToString(in.TableName) == runtime.SequenceTableName {
		d.serialValue++
		return &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{"value": &types.AttributeValueMemberN{Value: strconv.FormatInt(d.serialValue, 10)}}}, nil
	}
	return &dynamodb.UpdateItemOutput{}, nil
}
func (d *generatedDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	d.lastDelete = in
	if d.deleteErr != nil {
		return nil, d.deleteErr
	}
	return &dynamodb.DeleteItemOutput{}, nil
}
func (d *generatedDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	d.lastQuery = in
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	return &dynamodb.QueryOutput{Items: d.queryItems[aws.ToString(in.TableName)], LastEvaluatedKey: map[string]types.AttributeValue{"cursor": &types.AttributeValueMemberS{Value: "next"}}}, nil
}
func (d *generatedDynamo) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	d.lastScan = in
	if d.scanErr != nil {
		return nil, d.scanErr
	}
	return &dynamodb.ScanOutput{Items: d.scanItems[aws.ToString(in.TableName)]}, nil
}
func (d *generatedDynamo) TransactGetItems(_ context.Context, in *dynamodb.TransactGetItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactGetItemsOutput, error) {
	d.lastRead = in
	if d.transactGetErr != nil {
		return nil, d.transactGetErr
	}
	out := &dynamodb.TransactGetItemsOutput{}
	for _, tx := range in.TransactItems {
		table := aws.ToString(tx.Get.TableName)
		id := generatedKeyValue(tx.Get.Key["id"])
		out.Responses = append(out.Responses, types.ItemResponse{Item: d.records[table+"/"+id]})
	}
	return out, nil
}
func (d *generatedDynamo) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	d.lastWrite = in
	if d.transactWriteErr != nil {
		return nil, d.transactWriteErr
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func newGeneratedDynamo() *generatedDynamo {
	return &generatedDynamo{records: map[string]map[string]types.AttributeValue{}, getErrors: map[string]error{}, queryItems: map[string][]map[string]types.AttributeValue{}, scanItems: map[string][]map[string]types.AttributeValue{}}
}
func generatedKeyValue(av types.AttributeValue) string {
	switch v := av.(type) {
	case *types.AttributeValueMemberS:
		return v.Value
	case *types.AttributeValueMemberN:
		return v.Value
	}
	return ""
}
func setGeneratedRecord(t *testing.T, d *generatedDynamo, table, id string, item map[string]types.AttributeValue) {
	t.Helper()
	d.records[table+"/"+id] = item
}
func generatedUser(t *testing.T, id string) map[string]types.AttributeValue {
	t.Helper()
	item, err := encodeUser(User{ID: id, Name: "Ada", Preferences: Preferences{Theme: "dark", Digest: true}, Settings: Preferences{Theme: "light"}, OptionalPreferences: &Preferences{Theme: "pending"}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func generatedPost(t *testing.T, id, author string, editor *string) map[string]types.AttributeValue {
	t.Helper()
	now := time.Date(2025, 4, 5, 6, 7, 8, 0, time.UTC)
	item, err := encodePost(Post{ID: id, AuthorID: author, EditorID: editor, Title: "typed", CreatedAt: now, EditTimes: []time.Time{now}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func generatedSequenceItem(t *testing.T, id int64) map[string]types.AttributeValue {
	t.Helper()
	item, err := attributevalue.MarshalMap(SequenceItem{ID: id, Value: "sequence"})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func svalue(v string) *string { return &v }

func TestGeneratedCodecsCoverJSONDateTimeAndErrors(t *testing.T) {
	if _, err := decodeSequenceItem(map[string]types.AttributeValue{"id": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}}); err == nil {
		t.Fatal("expected sequence ID decode failure")
	}
	if _, err := keyUser(UserKey{ID: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := keyPost(PostKey{ID: "p"}); err != nil {
		t.Fatal(err)
	}
	var cycle ThemeOption
	cycle.Next = &cycle
	if _, err := encodeUser(User{Preferences: Preferences{Alternate: &cycle}}); err == nil || !strings.Contains(err.Error(), "User.preferences") {
		t.Fatalf("cyclic preferences: %v", err)
	}
	if _, err := encodeUser(User{Settings: Preferences{Alternate: &cycle}}); err == nil || !strings.Contains(err.Error(), "User.settings") {
		t.Fatalf("cyclic settings: %v", err)
	}
	cyclicPrefs := Preferences{Alternate: &cycle}
	if _, err := encodeUser(User{OptionalPreferences: &cyclicPrefs}); err == nil || !strings.Contains(err.Error(), "User.optionalPreferences") {
		t.Fatalf("cyclic optional JSON: %v", err)
	}
	base := map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "u"}, "name": &types.AttributeValueMemberS{Value: "Ada"}}
	if _, err := decodeUser(base); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"preferences", "settings", "optionalPreferences"} {
		wrong := map[string]types.AttributeValue{}
		for k, v := range base {
			wrong[k] = v
		}
		wrong[field] = &types.AttributeValueMemberN{Value: "1"}
		if _, err := decodeUser(wrong); err == nil || !strings.Contains(err.Error(), "User."+field) {
			t.Errorf("wrong JSON storage type %s: %v", field, err)
		}
		badJSON := map[string]types.AttributeValue{}
		for k, v := range base {
			badJSON[k] = v
		}
		badJSON[field] = &types.AttributeValueMemberS{Value: "{"}
		if _, err := decodeUser(badJSON); err == nil || !strings.Contains(err.Error(), "User."+field) {
			t.Errorf("bad JSON %s: %v", field, err)
		}
	}
	wrongPlain := map[string]types.AttributeValue{"id": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}}
	if _, err := decodeUser(wrongPlain); err == nil {
		t.Fatal("expected plain user decode failure")
	}
	when := time.Date(2026, 2, 3, 4, 5, 6, 0, time.FixedZone("east", 3600))
	later := when.Add(time.Hour)
	item, err := encodePost(Post{ID: "p", AuthorID: "u", CreatedAt: when, EditTimes: []time.Time{when, later}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := item["createdAt"].(*types.AttributeValueMemberS); !ok {
		t.Fatal("createdAt must be encoded as a string")
	}
	if _, err := decodePost(item); err != nil {
		t.Fatal(err)
	}
	empty := map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "p"}}
	if _, err := decodePost(empty); err != nil {
		t.Fatal(err)
	}
	wrongDate := map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "p"}, "createdAt": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}}
	if _, err := decodePost(wrongDate); err == nil {
		t.Fatal("expected DateTime AV type error")
	}
	badDate := map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "p"}, "createdAt": &types.AttributeValueMemberS{Value: "not-a-date"}}
	if _, err := decodePost(badDate); err == nil || !strings.Contains(err.Error(), "Post.createdAt") {
		t.Fatalf("bad timestamp: %v", err)
	}
	badList, err := attributevalue.Marshal([]string{"not-a-date"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePost(map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "p"}, "editTimes": badList}); err == nil || !strings.Contains(err.Error(), "Post.editTimes") {
		t.Fatalf("bad timestamp list: %v", err)
	}
	wrongList := map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "p"}, "editTimes": &types.AttributeValueMemberS{Value: "x"}}
	if _, err := decodePost(wrongList); err == nil {
		t.Fatal("expected list AV type error")
	}
	wrongPlainPost := map[string]types.AttributeValue{"id": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}}
	if _, err := decodePost(wrongPlainPost); err == nil {
		t.Fatal("expected plain post decode failure")
	}
}

func TestGeneratedClientCRUDQueriesAndOperations(t *testing.T) {
	ctx := context.Background()
	d := newGeneratedDynamo()
	c := NewClient(d)
	u := generatedUser(t, "u1")
	p := generatedPost(t, "p1", "u1", nil)
	setGeneratedRecord(t, d, "users", "u1", u)
	setGeneratedRecord(t, d, "posts", "p1", p)
	if _, err := c.CreateUser(ctx, User{ID: "u2", Name: "Bea"}); err != nil {
		t.Fatal(err)
	}
	if d.lastPut == nil || aws.ToString(d.lastPut.TableName) != "users" {
		t.Fatal("CreateUser")
	}
	generatedClient := NewClientWithIDGenerator(d, func(strategy string) (string, error) {
		if strategy != "uuidv7" {
			t.Fatalf("ID strategy %q", strategy)
		}
		return "auto-user", nil
	})
	createdUser, err := generatedClient.CreateUser(ctx, User{Name: "Auto"})
	if err != nil || createdUser.ID != "auto-user" {
		t.Fatalf("generated user %#v %v", createdUser, err)
	}
	createdDefault, err := NewClientWithIDGenerator(d, nil).CreateUser(ctx, User{Name: "Default generator"})
	if err != nil || createdDefault.ID == "" {
		t.Fatalf("default generator %#v %v", createdDefault, err)
	}
	generatorFailure := errors.New("entropy")
	_, err = NewClientWithIDGenerator(d, func(string) (string, error) { return "", generatorFailure }).CreateUser(ctx, User{Name: "Failure"})
	if !errors.Is(err, generatorFailure) {
		t.Fatalf("ID generation error: %v", err)
	}
	_, err = NewClientWithIDGenerator(d, func(string) (string, error) { return "", generatorFailure }).CreatePost(ctx, Post{AuthorID: "u1"})
	if !errors.Is(err, generatorFailure) {
		t.Fatalf("ObjectID generation error: %v", err)
	}
	d.putErr = errors.New("put user")
	if _, err := c.CreateUser(ctx, User{ID: "u-error"}); err == nil {
		t.Fatal("CreateUser should return DynamoDB error")
	}
	d.putErr = nil
	if got, ok, err := c.GetUser(ctx, UserKey{ID: "u1"}); err != nil || !ok || got.Name != "Ada" {
		t.Fatalf("GetUser %#v %v %v", got, ok, err)
	}
	if _, ok, err := c.GetUser(ctx, UserKey{ID: "missing"}); err != nil || ok {
		t.Fatalf("GetUser missing %v %v", ok, err)
	}
	d.getErrors["users"] = errors.New("read user")
	if _, _, err := c.GetUser(ctx, UserKey{ID: "u1"}); err == nil {
		t.Fatal("GetUser error")
	}
	delete(d.getErrors, "users")
	if err := c.DeleteUser(ctx, UserKey{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if page, err := c.ScanUser(ctx, runtime.PageRequest{Limit: 4}); err != nil || page.Items == nil {
		t.Fatalf("ScanUser %v %v", page, err)
	}
	if err := c.UpdateUser(ctx, UserKey{ID: "u1"}, UserUpdate{SetName: svalue("Grace"), SetPreferences: &Preferences{Theme: "blue"}, SetSettings: &Preferences{Theme: "red"}, SetOptionalPreferences: &Preferences{Theme: "pending"}, SetNickname: svalue("G")}); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateUser(ctx, UserKey{ID: "u1"}, UserUpdate{ClearNickname: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateUser(ctx, UserKey{ID: "u1"}, UserUpdate{ClearOptionalPreferences: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateUser(ctx, UserKey{ID: "u1"}, UserUpdate{SetNickname: svalue("x"), ClearNickname: true}); err == nil {
		t.Fatal("set+clear user optional")
	}
	if err := c.UpdateUser(ctx, UserKey{ID: "u1"}, UserUpdate{SetOptionalPreferences: &Preferences{}, ClearOptionalPreferences: true}); err == nil {
		t.Fatal("set+clear JSON optional")
	}
	var self ThemeOption
	self.Next = &self
	cyclic := Preferences{Alternate: &self}
	for _, patch := range []UserUpdate{{SetPreferences: &cyclic}, {SetSettings: &cyclic}, {SetOptionalPreferences: &cyclic}} {
		if err := c.UpdateUser(ctx, UserKey{ID: "u1"}, patch); err == nil {
			t.Errorf("expected cyclic JSON update failure: %#v", patch)
		}
	}
	if _, err := c.QueryUserByID(ctx, "u1", runtime.PageRequest{Limit: 2}); err != nil {
		t.Fatal(err)
	}
	if d.lastQuery == nil || aws.ToString(d.lastQuery.TableName) != "users" {
		t.Fatal("QueryUserByID")
	}
	if _, err := c.CreatePost(ctx, Post{ID: "p2", AuthorID: "u1"}); err != nil {
		t.Fatal(err)
	}
	createdPost, err := NewClientWithIDGenerator(d, func(strategy string) (string, error) {
		if strategy != "objectid" {
			t.Fatalf("ID strategy %q", strategy)
		}
		return "0123456789abcdef01234567", nil
	}).CreatePost(ctx, Post{AuthorID: "u1"})
	if err != nil || createdPost.ID != "0123456789abcdef01234567" {
		t.Fatalf("generated post %#v %v", createdPost, err)
	}
	d.putErr = errors.New("put post")
	if _, err := c.CreatePost(ctx, Post{ID: "p-error"}); err == nil {
		t.Fatal("CreatePost should return DynamoDB error")
	}
	d.putErr = nil
	if got, ok, err := c.GetPost(ctx, PostKey{ID: "p1"}); err != nil || !ok || got.Title != "typed" {
		t.Fatalf("GetPost %#v %v %v", got, ok, err)
	}
	if _, ok, err := c.GetPost(ctx, PostKey{ID: "missing"}); err != nil || ok {
		t.Fatalf("GetPost missing %v %v", ok, err)
	}
	if err := c.DeletePost(ctx, PostKey{ID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if page, err := c.ScanPost(ctx, runtime.PageRequest{}); err != nil || page.Items == nil {
		t.Fatalf("ScanPost %v %v", page, err)
	}
	now := time.Now().UTC()
	dates := []time.Time{now, now.Add(time.Hour)}
	patch := PostUpdate{SetAuthorID: svalue("u2"), SetEditorID: svalue("u3"), SetTitle: svalue("updated"), SetCreatedAt: &now, SetEditTimes: &dates}
	if err := c.UpdatePost(ctx, PostKey{ID: "p1"}, patch); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdatePost(ctx, PostKey{ID: "p1"}, PostUpdate{ClearEditorID: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdatePost(ctx, PostKey{ID: "p1"}, PostUpdate{SetEditorID: svalue("u"), ClearEditorID: true}); err == nil {
		t.Fatal("set+clear post optional")
	}
	if _, err := c.QueryPostByID(ctx, "p1", runtime.PageRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.QueryPostUsingByAuthor(ctx, "u1", nil, runtime.PageRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.QueryPostUsingByAuthor(ctx, "u1", &PostByAuthorSort{Operator: runtime.Greater, Value: now}, runtime.PageRequest{}); err != nil {
		t.Fatal(err)
	}
	if d.lastQuery.IndexName == nil || aws.ToString(d.lastQuery.IndexName) != "ByAuthor" {
		t.Fatal("GSI query")
	}
	var ru User
	readUser, err := c.ReadUser(UserKey{ID: "u1"}, &ru)
	if err != nil {
		t.Fatal(err)
	}
	var rp Post
	readPost, err := c.ReadPost(PostKey{ID: "p1"}, &rp)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.TransactGet(ctx, readUser, readPost); err != nil {
		t.Fatal(err)
	}
	writeUser, err := c.WriteUser(User{ID: "u3"})
	if err != nil {
		t.Fatal(err)
	}
	deleteUser, err := c.DeleteUserOperation(UserKey{ID: "u3"})
	if err != nil {
		t.Fatal(err)
	}
	updateUser, err := c.UpdateUserOperation(UserKey{ID: "u1"}, UserUpdate{SetName: svalue("N"), SetPreferences: &Preferences{Theme: "one"}, SetSettings: &Preferences{Theme: "two"}, SetOptionalPreferences: &Preferences{Theme: "three"}, ClearNickname: true})
	if err != nil {
		t.Fatal(err)
	}
	updateUserClear, err := c.UpdateUserOperation(UserKey{ID: "u1"}, UserUpdate{ClearOptionalPreferences: true})
	if err != nil {
		t.Fatal(err)
	}
	updateUserNickname, err := c.UpdateUserOperation(UserKey{ID: "u1"}, UserUpdate{SetNickname: svalue("Nick")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateUserOperation(UserKey{ID: "u1"}, UserUpdate{SetNickname: svalue("x"), ClearNickname: true}); err == nil {
		t.Fatal("transactional user set+clear")
	}
	if _, err := c.UpdateUserOperation(UserKey{ID: "u1"}, UserUpdate{SetOptionalPreferences: &Preferences{}, ClearOptionalPreferences: true}); err == nil {
		t.Fatal("transactional JSON set+clear")
	}
	for _, patch := range []UserUpdate{{SetPreferences: &cyclic}, {SetSettings: &cyclic}, {SetOptionalPreferences: &cyclic}} {
		if _, err := c.UpdateUserOperation(UserKey{ID: "u1"}, patch); err == nil {
			t.Errorf("expected cyclic transaction JSON error: %#v", patch)
		}
	}
	writePost, err := c.WritePost(Post{ID: "p3", AuthorID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	deletePost, err := c.DeletePostOperation(PostKey{ID: "p3"})
	if err != nil {
		t.Fatal(err)
	}
	updatePost, err := c.UpdatePostOperation(PostKey{ID: "p1"}, patch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdatePostOperation(PostKey{ID: "p1"}, PostUpdate{SetEditorID: svalue("x"), ClearEditorID: true}); err == nil {
		t.Fatal("transactional post set+clear")
	}
	updatePostClear, err := c.UpdatePostOperation(PostKey{ID: "p1"}, PostUpdate{ClearEditorID: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.TransactWrite(ctx, writeUser, deleteUser, updateUser, updateUserClear, updateUserNickname, writePost, deletePost, updatePost, updatePostClear); err != nil {
		t.Fatal(err)
	}
	if d.lastWrite == nil || len(d.lastWrite.TransactItems) != 9 {
		t.Fatal("TransactWrite")
	}
	seq, err := c.CreateSequenceItem(ctx, SequenceItem{Value: "first"})
	if err != nil || seq.ID != 1 {
		t.Fatalf("serial create %#v %v", seq, err)
	}
	seq, err = c.CreateSequenceItem(ctx, SequenceItem{ID: 42, Value: "explicit"})
	if err != nil || seq.ID != 42 || d.serialValue != 1 {
		t.Fatalf("explicit serial %#v %v count=%d", seq, err, d.serialValue)
	}
	setGeneratedRecord(t, d, "sequence_items", "42", generatedSequenceItem(t, 42))
	d.updateErr = errors.New("sequence update")
	if _, err := c.CreateSequenceItem(ctx, SequenceItem{Value: "failure"}); err == nil {
		t.Fatal("serial allocation should return update error")
	}
	d.updateErr = nil
	d.putErr = errors.New("put sequence")
	if _, err := c.CreateSequenceItem(ctx, SequenceItem{ID: 43, Value: "failure"}); err == nil {
		t.Fatal("CreateSequence should return DynamoDB error")
	}
	d.putErr = nil
	if got, ok, err := c.GetSequenceItem(ctx, SequenceItemKey{ID: 42}); err != nil || !ok || got.ID != 42 {
		t.Fatalf("GetSequence %#v %v %v", got, ok, err)
	}
	if err := c.DeleteSequenceItem(ctx, SequenceItemKey{ID: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScanSequenceItem(ctx, runtime.PageRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateSequenceItem(ctx, SequenceItemKey{ID: 42}, SequenceItemUpdate{SetValue: svalue("next")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.QuerySequenceItemByID(ctx, 42, runtime.PageRequest{}); err != nil {
		t.Fatal(err)
	}
	var seqRead SequenceItem
	readSeq, err := c.ReadSequenceItem(SequenceItemKey{ID: 42}, &seqRead)
	if err != nil {
		t.Fatal(err)
	}
	writeSeq, err := c.WriteSequenceItem(SequenceItem{ID: 44, Value: "write"})
	if err != nil {
		t.Fatal(err)
	}
	delSeq, err := c.DeleteSequenceItemOperation(SequenceItemKey{ID: 42})
	if err != nil {
		t.Fatal(err)
	}
	patchSeq, err := c.UpdateSequenceItemOperation(SequenceItemKey{ID: 42}, SequenceItemUpdate{SetValue: svalue("patch")})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.TransactGet(ctx, readSeq); err != nil {
		t.Fatal(err)
	}
	if err := c.TransactWrite(ctx, writeSeq, delSeq, patchSeq); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedClientRelationLoadingOutcomes(t *testing.T) {
	ctx := context.Background()
	d := newGeneratedDynamo()
	c := NewClient(d)
	setGeneratedRecord(t, d, "users", "u1", generatedUser(t, "u1"))
	setGeneratedRecord(t, d, "users", "u2", generatedUser(t, "u2"))
	setGeneratedRecord(t, d, "posts", "p1", generatedPost(t, "p1", "u1", nil))
	setGeneratedRecord(t, d, "posts", "p2", generatedPost(t, "p2", "u1", svalue("u2")))
	d.queryItems["posts"] = []map[string]types.AttributeValue{generatedPost(t, "p3", "u1", nil)}
	if got, err := c.GetUserWithPosts(ctx, UserKey{ID: "u1"}, runtime.PageRequest{Limit: 3}); err != nil || len(got.Posts.Items) != 1 {
		t.Fatalf("user posts %#v %v", got, err)
	}
	if _, err := c.GetUserWithPosts(ctx, UserKey{ID: "missing"}, runtime.PageRequest{}); err == nil {
		t.Fatal("missing user")
	}
	d.getErrors["users"] = errors.New("get user")
	if _, err := c.GetUserWithPosts(ctx, UserKey{ID: "u1"}, runtime.PageRequest{}); err == nil {
		t.Fatal("user get error")
	}
	delete(d.getErrors, "users")
	d.queryErr = errors.New("query posts")
	if _, err := c.GetUserWithPosts(ctx, UserKey{ID: "u1"}, runtime.PageRequest{}); err == nil {
		t.Fatal("posts query error")
	}
	d.queryErr = nil
	if got, err := c.GetPostWithAuthor(ctx, PostKey{ID: "p1"}); err != nil || got.Author.ID != "u1" {
		t.Fatalf("post author %#v %v", got, err)
	}
	if _, err := c.GetPostWithAuthor(ctx, PostKey{ID: "missing"}); err == nil {
		t.Fatal("missing post")
	}
	d.getErrors["posts"] = errors.New("get post")
	if _, err := c.GetPostWithAuthor(ctx, PostKey{ID: "p1"}); err == nil {
		t.Fatal("post get error")
	}
	delete(d.getErrors, "posts")
	delete(d.records, "users/u1")
	if _, err := c.GetPostWithAuthor(ctx, PostKey{ID: "p1"}); err == nil {
		t.Fatal("missing required author")
	}
	setGeneratedRecord(t, d, "users", "u1", generatedUser(t, "u1"))
	d.getErrors["users"] = errors.New("get author")
	if _, err := c.GetPostWithAuthor(ctx, PostKey{ID: "p1"}); err == nil {
		t.Fatal("author get error")
	}
	delete(d.getErrors, "users")
	if got, err := c.GetPostWithEditor(ctx, PostKey{ID: "p1"}); err != nil || got.Editor != nil {
		t.Fatalf("nil optional editor %#v %v", got, err)
	}
	if got, err := c.GetPostWithEditor(ctx, PostKey{ID: "p2"}); err != nil || got.Editor == nil || got.Editor.ID != "u2" {
		t.Fatalf("editor %#v %v", got, err)
	}
	if _, err := c.GetPostWithEditor(ctx, PostKey{ID: "missing"}); err == nil {
		t.Fatal("missing editor post")
	}
	d.getErrors["posts"] = errors.New("editor post read")
	if _, err := c.GetPostWithEditor(ctx, PostKey{ID: "p2"}); err == nil {
		t.Fatal("editor parent read error")
	}
	delete(d.getErrors, "posts")
	delete(d.records, "users/u2")
	if _, err := c.GetPostWithEditor(ctx, PostKey{ID: "p2"}); err == nil {
		t.Fatal("missing optional related user")
	}
	setGeneratedRecord(t, d, "users", "u2", generatedUser(t, "u2"))
	d.getErrors["users"] = errors.New("get editor")
	if _, err := c.GetPostWithEditor(ctx, PostKey{ID: "p2"}); err == nil {
		t.Fatal("editor get error")
	}
}
