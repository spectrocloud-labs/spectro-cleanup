package main

import (
	"context"
	"os"
	"testing"
	"time"

	cleanv1 "buf.build/gen/go/spectrocloud/spectro-cleanup/protocolbuffers/go/cleanup/v1"
	"connectrpc.com/connect"
)

func TestReadConfig(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		expectedOutput []byte
		expectedError  bool
	}{
		{
			name:           "non existing file",
			path:           "tmp/nonexistingfile.json",
			expectedOutput: nil,
			expectedError:  false,
		},
		{
			name: "existing file",
			path: "/tmp/existingfile.json",
			expectedOutput: []byte(`[
      "/host/etc/cni/net.d/00-multus.conf",
      "/host/opt/cni/bin/multus"
    ]`),
			expectedError: false,
		},
	}

	// Setup a temporary file for testing
	fileContent := []byte(`[
      "/host/etc/cni/net.d/00-multus.conf",
      "/host/opt/cni/bin/multus"
    ]`)
	tmpFile, err := os.CreateTemp("", "existingfile.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write(fileContent)
	tmpFile.Close()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.path == "/tmp/existingfile.json" {
				tt.path = tmpFile.Name()
			}
			output := readConfig(tt.path, FilesToDelete)

			if string(output) != string(tt.expectedOutput) {
				t.Errorf("expected output %s, got %s", tt.expectedOutput, output)
			}
		})
	}
}

func TestFinalizeCleanup(t *testing.T) {
	server := &cleanupServiceServer{}
	ctx := context.TODO()
	req := connect.NewRequest(&cleanv1.FinalizeCleanupRequest{})

	tests := []struct {
		name        string
		testChan    chan bool
		expectedErr error
	}{
		{
			name:     "valid notification channel",
			testChan: make(chan bool),
		},
		{
			name:        "nil notification channel",
			testChan:    nil,
			expectedErr: ErrIllegalCleanupNotification,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notif = &tt.testChan

			go func() {
				<-time.After(1 * time.Second)
				<-tt.testChan
				close(tt.testChan)
			}()

			resp, err := server.FinalizeCleanup(ctx, req)
			if err != nil && tt.expectedErr == nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if err == nil && tt.expectedErr != nil {
				t.Fatalf("expected error %v, got nil", tt.expectedErr)
			}
			if err != nil && tt.expectedErr != nil && err.Error() != tt.expectedErr.Error() {
				t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
			}

			if resp == nil {
				t.Fatalf("expected response, got nil")
			}
		})
	}
}
