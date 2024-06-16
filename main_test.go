package main

import (
	"testing"
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

		expectedCleanup            int64
		expectedFileConfigPath     string
		expectedResourceConfigPath string
		expectedSaName             string
		expectedRoleName           string
		expectedRoleBindingName    string
		expectedGRPC               bool
	}{
		{
			name:                       "no vars set",
			expectedCleanup:            30,
			expectedFileConfigPath:     "/tmp/spectro-cleanup/file-config.json",
			expectedResourceConfigPath: "/tmp/spectro-cleanup/resource-config.json",
			expectedSaName:             "spectro-cleanup",
			expectedRoleName:           "spectro-cleanup-role",
			expectedRoleBindingName:    "spectro-cleanup-rolebinding",
			expectedGRPC:               false,
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

			expectedCleanup:            100,
			expectedFileConfigPath:     "new-file-config-path.json",
			expectedResourceConfigPath: "new-resource-config-path.json",
			expectedSaName:             "new-sa-name",
			expectedRoleName:           "new-role-name",
			expectedRoleBindingName:    "new-role-binding-name",
			expectedGRPC:               true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// set all vars to default values
			cleanupSeconds = 0
			enableGrpcServer = false

			// initialize env vars
			cleanupSecondsStr = tt.cleanupSecondsStr
			fileConfigPath = tt.fileConfigPath
			resourceConfigPath = tt.resourceConfigPath
			saName = tt.saName
			roleName = tt.roleName
			roleBindingName = tt.roleBindingName
			enableGrpcServerStr = tt.enableGrpcServerStr
			grpcPortStr = tt.grcpPortStr

			initConfig()

			if cleanupSeconds != tt.expectedCleanup {
				t.Errorf("expected cleanupSeconds %d, got %d", tt.expectedCleanup, cleanupSeconds)
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
