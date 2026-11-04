// Package transaction shows a conditional publishing transaction assembled from
// independently generated admin and learner clients.
package transaction

import (
	"context"

	"github.com/liberocks/dynamox/examples/transaction/admin"
	"github.com/liberocks/dynamox/examples/transaction/learner"
	"github.com/liberocks/dynamox/runtime"
)

func Publish(ctx context.Context, adminClient *admin.Client, learnerClient *learner.Client, draftKey admin.DraftKey, content learner.Content) error {
	published := "published"
	draftUpdate, err := adminClient.UpdateDraftOperationWithOptions(
		draftKey,
		admin.DraftUpdate{SetStatus: &published},
		runtime.WriteOptions{Condition: &runtime.Expression{
			Text:   "#status = :unpublished",
			Names:  map[string]string{"#status": "status"},
			Values: map[string]any{":unpublished": "unpublished"},
		}},
	)
	if err != nil {
		return err
	}
	contentCreate, err := learnerClient.CreateContentOperation(content)
	if err != nil {
		return err
	}
	return adminClient.TransactWrite(ctx, draftUpdate, contentCreate)
}
