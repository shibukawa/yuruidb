package aws

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Expected signatures are from AWS's published S3 SigV4 worked examples:
// https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sig-v4-header-based-auth.html
func TestPublishedAWSVectors(t *testing.T) {
	c := Credentials{AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct{ path, rangeHeader, signature string }{
		{"/?lifecycle", "", "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"/?max-keys=2&prefix=J", "", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
		{"/test.txt", "bytes=0-9", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
	} {
		t.Run(test.path, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com"+test.path, nil)
			if test.rangeHeader != "" {
				r.Header.Set("Range", test.rangeHeader)
			}
			if err := Sign(r, c, "us-east-1", "s3", now); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(r.Header.Get("Authorization"), "Signature="+test.signature) {
				t.Fatalf("signature: %s", r.Header.Get("Authorization"))
			}
		})
	}
}
func TestSignerConcurrentEnvironmentAndSession(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "temporary")
	sign := Signer("us-east-1", "s3", nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := http.NewRequest("PUT", "https://example.invalid/bucket/key", strings.NewReader("body"))
			r.Header.Set("If-Match", "revision")
			if err := sign(r); err != nil {
				t.Error(err)
			}
			if r.Header.Get("X-Amz-Security-Token") != "temporary" || !strings.Contains(r.Header.Get("Authorization"), "if-match") {
				t.Error("missing signed session/condition")
			}
		}()
	}
	wg.Wait()
}
