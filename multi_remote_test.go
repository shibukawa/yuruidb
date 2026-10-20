package yuruidb_test

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	awsauth "github.com/shibukawa/yuruidb/auth/aws"
	_ "github.com/shibukawa/yuruidb/drivers/dynamodb"
	_ "github.com/shibukawa/yuruidb/drivers/firebase"
	_ "github.com/shibukawa/yuruidb/drivers/firestore"
	_ "github.com/shibukawa/yuruidb/drivers/gcs"
	_ "github.com/shibukawa/yuruidb/drivers/r2"
	_ "github.com/shibukawa/yuruidb/drivers/s3"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"testing"
)

func TestRemoteStoreMultipleTypes(t *testing.T) {
	for _, kind := range []string{"s3", "r2", "gcs", "dynamodb", "firestore", "firebase"} {
		t.Run(kind, func(t *testing.T) {
			server := protocoltest.New(t, kind)
			config := yuruidb.Config{"Endpoint": server.URL}
			switch kind {
			case "s3", "r2":
				config["Bucket"] = "bucket"
				config["Credentials"] = awsauth.Static(awsauth.Credentials{AccessKey: "test", SecretKey: "test"})
			case "gcs":
				config["Bucket"] = "bucket"
				config["Token"] = auth.StaticToken("test-token")
			case "dynamodb":
				config["Table"] = "table"
				config["Credentials"] = awsauth.Static(awsauth.Credentials{AccessKey: "test", SecretKey: "test"})
			case "firestore":
				config["Endpoint"] = server.URL + "/v1"
				config["Project"] = "project"
				config["Token"] = auth.StaticToken("test-token")
			case "firebase":
				config["Token"] = auth.StaticToken("test-token")
			}
			ctx := context.Background()
			s, err := yuruidb.Open(ctx, kind, config)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.Bind[Waiting]("jobs"); err != nil {
				t.Fatal(err)
			}
			if err = s.Bind[Done]("done"); err != nil {
				t.Fatal(err)
			}
			waiting, err := s.Insert(ctx, "host.local", Waiting{Text: "waiting"})
			if err != nil {
				t.Fatal(err)
			}
			done, err := s.Insert(ctx, "host.local", Done{Text: "done"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Get[Waiting](ctx, "host.local")
			if err != nil || got != waiting {
				t.Fatalf("get: %+v %v", got, err)
			}
			page, err := s.List[Done](ctx, yuruidb.ListOptions{})
			if err != nil || len(page.Records) != 1 || page.Records[0] != done {
				t.Fatalf("list: %+v %v", page, err)
			}
			if err = s.Delete[Waiting](ctx, "host.local", waiting.Revision()); err != nil {
				t.Fatal(err)
			}
			gotDone, err := s.Get[Done](ctx, "host.local")
			if err != nil || gotDone != done {
				t.Fatalf("other collection changed: %+v %v", gotDone, err)
			}
		})
	}
}
