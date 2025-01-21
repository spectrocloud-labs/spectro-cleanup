package main

import (
	"context"
	"os"
	"testing"
	"time"

	cleanv1 "buf.build/gen/go/spectrocloud/spectro-cleanup/protocolbuffers/go/cleanup/v1"
	"connectrpc.com/connect"
)

func TestInitConfig(t *testing.T) {
	tests := []struct {
		name string

		cleanupSecondsStr   string
		fileConfigPath      string
		resourceConfigPath  string
		saName              string
		roleName            string
		roleBindingName     string
		enableGrpcServerStr string
		grcpPortStr         string
		deletionIntervalStr string
		deletionTimeoutStr  string
		blockingDeletionStr string

		expectedCleanup            time.Duration
		expectedFileConfigPath     string
		expectedResourceConfigPath string
		expectedSaName             string
		expectedRoleName           string
		expectedRoleBindingName    string
		expectedGRPC               bool
		expectedDeletionInterval   time.Duration
		expectedDeletionTimeout    time.Duration
		expectedBlockingDeletion   bool
	}{
		{
			name:                       "no vars set",
			expectedCleanup:            30 * time.Second,
			expectedFileConfigPath:     "/tmp/spectro-cleanup/file-config.json",
			expectedResourceConfigPath: "/tmp/spectro-cleanup/resource-config.json",
			expectedSaName:             "spectro-cleanup",
			expectedRoleName:           "spectro-cleanup-role",
			expectedRoleBindingName:    "spectro-cleanup-rolebinding",
			expectedGRPC:               false,
			expectedBlockingDeletion:   false,
			expectedDeletionInterval:   2 * time.Second,
			expectedDeletionTimeout:    300 * time.Second,
		},
		{
			name:                "all vars set to non default values",
			cleanupSecondsStr:   "100",
			fileConfigPath:      "new-file-config-path.json",
			resourceConfigPath:  "new-resource-config-path.json",
			saName:              "new-sa-name",
			roleName:            "new-role-name",
			roleBindingName:     "new-role-binding-name",
			enableGrpcServerStr: "true",
			grcpPortStr:         "1234",
			deletionIntervalStr: "10",
			deletionTimeoutStr:  "100",
			blockingDeletionStr: "true",

			expectedCleanup:            100 * time.Second,
			expectedFileConfigPath:     "new-file-config-path.json",
			expectedResourceConfigPath: "new-resource-config-path.json",
			expectedSaName:             "new-sa-name",
			expectedRoleName:           "new-role-name",
			expectedRoleBindingName:    "new-role-binding-name",
			expectedGRPC:               true,
			expectedDeletionInterval:   10 * time.Second,
			expectedDeletionTimeout:    100 * time.Second,
			expectedBlockingDeletion:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// set all vars to default values
			cleanupTimeout = 0
			enableGrpcServer = false

			// initialize env vars
			cleanupTimeoutStr = tt.cleanupSecondsStr
			fileConfigPath = tt.fileConfigPath
			resourceConfigPath = tt.resourceConfigPath
			saName = tt.saName
			roleName = tt.roleName
			roleBindingName = tt.roleBindingName
			enableGrpcServerStr = tt.enableGrpcServerStr
			grpcPortStr = tt.grcpPortStr

			initConfig()

			if cleanupTimeout != tt.expectedCleanup {
				t.Errorf("expected cleanupSeconds %d, got %d", tt.expectedCleanup, cleanupTimeout)
			}
			if fileConfigPath != tt.expectedFileConfigPath {
				t.Errorf("expected fileConfigPath %s, got %s", tt.expectedFileConfigPath, fileConfigPath)
			}
			if resourceConfigPath != tt.expectedResourceConfigPath {
				t.Errorf("expected resourceConfigPath %s, got %s", tt.expectedResourceConfigPath, resourceConfigPath)
			}
			if saName != tt.expectedSaName {
				t.Errorf("expected saName %s, got %s", tt.expectedSaName, saName)
			}
			if roleName != tt.expectedRoleName {
				t.Errorf("expected roleName %s, got %s", tt.expectedRoleName, roleName)
			}
			if roleBindingName != tt.expectedRoleBindingName {
				t.Errorf("expected roleBindingName %s, got %s", tt.expectedRoleBindingName, roleBindingName)
			}
			if enableGrpcServer != tt.expectedGRPC {
				t.Errorf("expected enableGrpcServer %v, got %v", tt.expectedGRPC, enableGrpcServer)
			}
		})
	}
}

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
