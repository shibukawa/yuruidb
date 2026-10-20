package yuruidb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shibukawa/yuruidb"
)

func TestUnknownOutcomeDoesNotClassifyAsDefinitiveFailure(t *testing.T) {
	cause := errors.Join(yuruidb.ErrConflict, yuruidb.ErrAlreadyExists, context.DeadlineExceeded)
	for _, err := range []error{yuruidb.OutcomeUnknown(cause), &yuruidb.MutationError{Cause: cause}} {
		if !errors.Is(err, yuruidb.ErrOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, yuruidb.ErrConflict) || errors.Is(err, yuruidb.ErrAlreadyExists) {
			t.Fatalf("unsafe error classification: %v", err)
		}
	}
}
