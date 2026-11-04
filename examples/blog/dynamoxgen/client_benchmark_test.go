package dynamoxgen

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/liberocks/dynamox/provision"
	"github.com/liberocks/dynamox/runtime"
)

func newBenchmarkClient(b *testing.B) (context.Context, *Client, UserKey, PostKey, Post) {
	b.Helper()
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		b.Skip("set DYNAMODB_ENDPOINT to run DynamoDB emulator benchmarks")
	}

	ctx := context.Background()
	db := dynamodb.New(dynamodb.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("local", "local", ""),
		BaseEndpoint: aws.String(endpoint),
	})
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userTableName := "dynamox_bench_users_" + suffix
	postTableName := "dynamox_bench_posts_" + suffix
	created, err := provision.Sync(ctx, db, []provision.Table{
		{
			Name:      userTableName,
			Partition: provision.Key{Name: "id", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash},
		},
		{
			Name:      postTableName,
			Partition: provision.Key{Name: "id", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash},
			Indexes: []provision.Index{{
				Name:      "ByAuthor",
				Partition: provision.Key{Name: "authorId", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeHash},
				Sort:      provision.Key{Name: "createdAt", Type: types.ScalarAttributeTypeS, Kind: types.KeyTypeRange},
			}},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	if len(created) != 2 {
		b.Fatalf("expected fresh benchmark tables, created %v", created)
	}
	b.Cleanup(func() {
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(created[i])}); err != nil {
				b.Logf("delete benchmark table %s: %v", created[i], err)
			}
		}
	})

	client := NewClient(db)
	// Generated table names come from the example schema; override them with
	// unique names so benchmark runs never reuse or delete application tables.
	client.user.Name = userTableName
	client.post.Name = postTableName

	userKey := UserKey{ID: "bench-user-" + suffix}
	postKey := PostKey{ID: "bench-post-" + suffix}
	if _, err := client.CreateUser(ctx, User{ID: userKey.ID, Name: "Benchmark user"}); err != nil {
		b.Fatal(err)
	}
	post := Post{
		ID:        postKey.ID,
		AuthorID:  userKey.ID,
		Title:     "Benchmark post",
		CreatedAt: time.Now().UTC(),
	}
	if _, err := client.CreatePost(ctx, post); err != nil {
		b.Fatal(err)
	}
	return ctx, client, userKey, postKey, post
}

func BenchmarkDynamoxGeneratedRelationsAndTransactions(b *testing.B) {
	ctx, client, userKey, postKey, post := newBenchmarkClient(b)

	b.Run("Relation/GetUserWithPosts", func(b *testing.B) {
		page := runtime.PageRequest{Limit: 10}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got, err := client.GetUserWithPosts(ctx, userKey, page)
			if err != nil || got.User.ID != userKey.ID || len(got.Posts.Items) != 1 {
				b.Fatalf("GetUserWithPosts result: value=%#v err=%v", got, err)
			}
		}
	})

	b.Run("Transaction/TransactGet", func(b *testing.B) {
		var user User
		var relatedPost Post
		readUser, err := client.ReadUser(userKey, &user)
		if err != nil {
			b.Fatal(err)
		}
		readPost, err := client.ReadPost(postKey, &relatedPost)
		if err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := client.TransactGet(ctx, readUser, readPost); err != nil {
				b.Fatal(err)
			}
			if user.ID != userKey.ID || relatedPost.ID != postKey.ID {
				b.Fatalf("TransactGet result: user=%#v post=%#v", user, relatedPost)
			}
		}
	})

	b.Run("Transaction/TransactWrite", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			name := "Transaction benchmark"
			updateUser, err := client.UpdateUserOperation(userKey, UserUpdate{SetName: &name})
			if err != nil {
				b.Fatal(err)
			}
			writePost, err := client.WritePost(post)
			if err != nil {
				b.Fatal(err)
			}
			if err := client.TransactWrite(ctx, updateUser, writePost); err != nil {
				b.Fatal(err)
			}
		}
	})
}
