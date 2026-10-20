package dynamodb_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/drivers/dynamodb"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.RawStore, yuruidb.RawStore) {
		server := protocoltest.New(t, "dynamodb")
		open := func() yuruidb.RawStore {
			s, err := dynamodb.OpenRaw(context.Background(), dynamodb.Config{Endpoint: server.URL, Region: "test-region", Table: "table", Collection: "items", Credentials: aws.Static(aws.Credentials{AccessKey: "key", SecretKey: "secret"})})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		}
		return open(), open()
	})
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestThrottlingRetryKeepsMutation(t *testing.T) {
	for _, code := range []string{"ThrottlingException", "ProvisionedThroughputExceededException", "RequestLimitExceeded", "ValidationException"} {
		t.Run(code, func(t *testing.T) {
			attempts, auths := 0, 0
			var first []byte
			s, err := dynamodb.OpenRaw(context.Background(), dynamodb.Config{Endpoint: "https://example.invalid", Table: "table", Collection: "items", Authorize: func(*http.Request) error { auths++; return nil }, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				attempts++
				body, _ := io.ReadAll(r.Body)
				if attempts == 1 {
					first = body
				} else if !bytes.Equal(body, first) {
					t.Error("mutation changed on retry")
				}
				status, reply := 400, `{"__type":"com.amazonaws.dynamodb.v20120810#`+code+`"}`
				if attempts > 1 {
					status, reply = 200, `{}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(reply))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, err = s.Insert(context.Background(), "job", []byte("initial"))
			if code == "ValidationException" {
				if err == nil || attempts != 1 {
					t.Fatalf("permanent response retried: attempts=%d err=%v", attempts, err)
				}
			} else if err != nil || attempts != 2 || auths != 2 {
				t.Fatalf("throttling retry: attempts=%d auths=%d err=%v", attempts, auths, err)
			}
		})
	}
}

func TestLostResponseThenConditionAndReadFailure(t *testing.T) {
	attempts := 0
	s, err := dynamodb.OpenRaw(context.Background(), dynamodb.Config{Endpoint: "https://example.invalid", Table: "table", Collection: "items", Authorize: func(*http.Request) error { return nil }, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		attempts++
		status, body := 503, `{}`
		if attempts == 2 {
			status, body = 400, `{"__type":"ConditionalCheckFailedException"}`
		}
		if strings.HasSuffix(r.Header.Get("X-Amz-Target"), "GetItem") {
			status = 403
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Insert(context.Background(), "job", []byte("attempt"))
	var attempt *yuruidb.MutationError
	if !errors.Is(err, yuruidb.ErrOutcomeUnknown) || errors.Is(err, yuruidb.ErrConflict) || errors.Is(err, yuruidb.ErrAlreadyExists) || !errors.As(err, &attempt) || attempts != 3 {
		t.Fatalf("unsafe outcome: attempts=%d err=%v", attempts, err)
	}
}
