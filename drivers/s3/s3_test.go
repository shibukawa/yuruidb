package s3_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/drivers/s3"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestHTTPConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.Store, yuruidb.Store) {
		server := protocoltest.New(t, "s3")
		open := func() yuruidb.Store {
			s, err := s3.Open(context.Background(), s3.Config{Endpoint: server.URL, Region: "test-region", Bucket: "bucket", Collection: "items", Credentials: aws.Static(aws.Credentials{AccessKey: "key", SecretKey: "secret", SessionToken: "session"})})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		}
		return open(), open()
	})
}

func TestProcessCAS(t *testing.T) {
	if endpoint := os.Getenv("YURUIDB_TEST_ENDPOINT"); endpoint != "" {
		s, err := s3.Open(context.Background(), s3.Config{Endpoint: endpoint, Bucket: "bucket", Collection: "items", Credentials: aws.Static(aws.Credentials{AccessKey: "key", SecretKey: "secret"})})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_, err = s.Update(context.Background(), os.Getenv("YURUIDB_TEST_ID"), []byte("child"), os.Getenv("YURUIDB_TEST_REVISION"))
		if err == nil {
			fmt.Println("CAS_WIN")
		} else if errors.Is(err, yuruidb.ErrConflict) {
			fmt.Println("CAS_CONFLICT")
		} else {
			t.Fatal(err)
		}
		return
	}
	server := protocoltest.New(t, "s3")
	s, err := s3.Open(context.Background(), s3.Config{Endpoint: server.URL, Bucket: "bucket", Collection: "items", Credentials: aws.Static(aws.Credentials{AccessKey: "key", SecretKey: "secret"})})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Insert(context.Background(), conformance.ID(t), []byte("parent"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessCAS$")
			cmd.Env = append(os.Environ(), "YURUIDB_TEST_ENDPOINT="+server.URL, "YURUIDB_TEST_ID="+r.ID, "YURUIDB_TEST_REVISION="+r.Revision)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("child: %s %v", out, err)
			}
			results <- string(out)
		}()
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for text := range results {
		if strings.Contains(text, "CAS_WIN") {
			wins++
		}
		if strings.Contains(text, "CAS_CONFLICT") {
			conflicts++
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("process outcomes: wins=%d conflicts=%d", wins, conflicts)
	}
}
