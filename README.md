[![Contributions Welcome](https://img.shields.io/badge/contributions-welcome-brightgreen.svg?style=flat)](https://github.com/spectrocloud-labs/spectro-cleanup/issues)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![codecov](https://codecov.io/github/spectrocloud-labs/spectro-cleanup/graph/badge.svg?token=Q15XUCRNCN)](https://codecov.io/github/spectrocloud-labs/spectro-cleanup)
[![Go Reference](https://pkg.go.dev/badge/github.com/spectrocloud-labs/spectro-cleanup.svg)](https://pkg.go.dev/github.com/spectrocloud-labs/spectro-cleanup)

# spectro-cleanup
A generic cleanup utility for removing arbitrary files from nodes and/or resources from a K8s cluster.

This tool can be deployed as a DaemonSet/Job/Pod. Simply create your config files and apply it on your K8s cluster.

## Configuration
### Examples
#### DaemonSet Configuration
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: spectro-cleanup-role
  namespace: kube-system
  annotations:
    "helm.sh/hook": pre-delete
  labels:
    app: {{ template "multus.name" . }}
    {{- include "multus.labels" . | indent 4 }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - serviceaccounts
  verbs:
  - '*'
- apiGroups:
  - apps
  resources:
  - daemonsets
  verbs:
  - '*'
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  - roles
  verbs:
  - '*'
---
kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: spectro-cleanup-rolebinding
  namespace: kube-system
  annotations:
    "helm.sh/hook": pre-delete
  labels:
    app: {{ template "multus.name" . }}
    {{- include "multus.labels" . | indent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: spectro-cleanup-role
subjects:
  - kind: ServiceAccount
    name: spectro-cleanup
    namespace: kube-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: spectro-cleanup
  namespace: kube-system
  annotations:
    "helm.sh/hook": pre-delete
  labels:
    app: {{ template "multus.name" . }}
    {{- include "multus.labels" . | indent 4 }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: spectro-cleanup-config
  namespace: kube-system
  annotations:
    "helm.sh/hook": pre-delete
  labels:
    app: {{ template "multus.name" . }}
    {{- include "multus.labels" . | indent 4 }}
data:
  file-config.json: |-
    [
      "/host/etc/cni/net.d/00-multus.conf",
      "/host/opt/cni/bin/multus"
    ]
  resource-config.json: |-
    [
      {
        "group": "",
        "version": "v1",
        "resource": "configmaps",
        "name": "spectro-cleanup-config",
        "namespace": "kube-system"
      },
      {
        "group": "apps",
        "version": "v1",
        "resource": "daemonsets",
        "name": "spectro-cleanup",
        "namespace": "kube-system"
      }
    ]
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: spectro-cleanup
  namespace: kube-system
  annotations:
    "helm.sh/hook": pre-delete
  labels:
    name: spectro-cleanup
    app: {{ template "multus.name" . }}
    release: {{ .Release.Name }}
    {{- include "multus.labels" . | indent 4 }}
spec:
  selector:
    matchLabels:
      name: spectro-cleanup
  template:
    metadata:
      labels:
        name: spectro-cleanup
    spec:
      hostNetwork: true
      nodeSelector:
        kubernetes.io/arch: amd64
      tolerations:
      - operator: Exists
        effect: NoSchedule
      serviceAccountName: spectro-cleanup
      containers:
      - name: spectro-cleanup
        image: gcr.io/spectro-images-public/release/spectro-cleanup:1.0.0
        command: ["/cleanup"]
        resources:
          requests:
            cpu: "10m"
            memory: "25Mi"
          limits:
            cpu: "20m"
            memory: "50Mi"
        securityContext:
          privileged: true
        volumeMounts:
        - name: spectro-cleanup-config
          mountPath: /tmp/spectro-cleanup
        - name: cni-bin
          mountPath: /host/opt/cni/bin
        - name: cni
          mountPath: /host/etc/cni/net.d
      volumes:
        - name: spectro-cleanup-config
          configMap:
            name: spectro-cleanup-config
            items:
            - key: file-config.json
              path: file-config.json
            - key: resource-config.json
              path: resource-config.json
        - name: cni-bin
          hostPath:
            path: /opt/cni/bin
        - name: cni
          hostPath:
            path: /etc/cni/net.d
```

#### Job Configuration
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: spectro-cleanup-role
  namespace: kube-system
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - serviceaccounts
  verbs:
  - '*'
- apiGroups:
  - batch
  resources:
  - jobs
  verbs:
  - '*'
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  - roles
  verbs:
  - '*'
---
kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: spectro-cleanup-rolebinding
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: spectro-cleanup-role
subjects:
  - kind: ServiceAccount
    name: spectro-cleanup
    namespace: kube-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: spectro-cleanup
  namespace: kube-system
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: spectro-cleanup-config
  namespace: kube-system
data:
  # multus files we want to delete
  file-config.json: |-
    [
      "/host/etc/cni/net.d/00-multus.conf",
      "/host/opt/cni/bin/multus"
    ]
  # spectro-cleanup resources we want to delete
  resource-config.json: |-
    [
      {
        "group": "",
        "version": "v1",
        "resource": "configmaps",
        "name": "spectro-cleanup-config",
        "namespace": "kube-system"
      },
      {
        "group": "batch",
        "version": "v1",
        "resource": "jobs",
        "name": "spectro-cleanup",
        "namespace": "kube-system"
      }
    ]
---
apiVersion: batch/v1
kind: Job
metadata:
  name: spectro-cleanup
  namespace: kube-system
spec:
  template:
    metadata:
      labels:
        name: spectro-cleanup
    spec:
      restartPolicy: Never
      serviceAccountName: spectro-cleanup
      containers:
      - name: spectro-cleanup
        image: gcr.io/spectro-images-public/release/spectro-cleanup:1.2.0
        command: ["/cleanup"]
        args:
        - --cleanup-timeout-seconds=10
        resources:
          requests:
            cpu: "10m"
            memory: "25Mi"
          limits:
            cpu: "20m"
            memory: "50Mi"
        securityContext:
          privileged: true
        volumeMounts:
        - name: spectro-cleanup-config
          mountPath: /tmp/spectro-cleanup
        - name: cni-bin
          mountPath: /host/opt/cni/bin
        - name: cni
          mountPath: /host/etc/cni/net.d
      volumes:
        - name: spectro-cleanup-config
          configMap:
            name: spectro-cleanup-config
            items:
            - key: file-config.json
              path: file-config.json
            - key: resource-config.json
              path: resource-config.json
        - name: cni-bin
          hostPath:
            path: /opt/cni/bin
        - name: cni
          hostPath:
            path: /etc/cni/net.d
```

### Configuration Notes

To ensure that spectro-cleanup itself is cleaned up after its finished getting rid of your chosed files/resources on your cluster, 
you'll need to ensure that the final objects in your `resource-config.json` are the spectro-cleanup `configmaps` and the `daemonset/job/pod`.
If there are any resources added to the `resource-config.json` _after_ the two aformentioned spectro-cleanup resources, they will not be cleaned up.

By default, delete operations for kubernetes resources are blocking. The cleanup process will poll until the resource is fully removed from the API server. You can customize this behaviour via `--blocking-deletion` (defaults to `true`). The polling interval and timeout default to 2s and 5m, respectively, and be customized via `--deletion-interval-seconds`, and `--deletion-timeout-seconds`.

You can also optionally configure a gRPC server to run as a part of spectro-cleanup. This server has a single endpoint, `FinalizeCleanup`.
When this server is configured, spectro-cleanup will be able to wait for a request that notifies it that it can finally clean itself up.
In this case, the `--cleanup-timeout-seconds` flag will have the fallback time to self destruct in the case that a request is never made to the `FinalizeCleanup` endpoint.

Below you can see an example of how to configure the gRPC server on your daemonset or job:
```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: validator-cleanup
  annotations:
    "helm.sh/hook": pre-delete
spec:
  template:
    metadata:
      labels:
        app: validator-cleanup-job
    spec:
      restartPolicy: Never
      serviceAccountName: spectro-cleanup
      containers:
      - name: validator-cleanup
        image: {{ required ".Values.cleanup.image is required!" .Values.cleanup.image }}
        command: ["/cleanup"]
        args:
        - --cleanup-timeout-seconds=300
        {{- if .Values.cleanup.grpcServerEnabled }}
        - --enable-grpc-server
        - --grpc-port={{ required ".Values.cleanup.port is required!" .Values.cleanup.port | toString | quote }}
        {{- end }}
        resources:
          requests:
            cpu: "10m"
            memory: "50Mi"
          limits:
            cpu: "100m"
            memory: "100Mi"
        volumeMounts:
        - name: validator-cleanup-config
          mountPath: /tmp/spectro-cleanup
      volumes:
        - name: validator-cleanup-config
          configMap:
            name: validator-cleanup-config
            items:
            - key: resource-config.json
              path: resource-config.json

```
The main things to note here are that all three of the `--enable-grpc-server`, `--grpc-port`, and `--cleanup-timeout-seconds` flags are set.
You can see more about how this configuration is setup in the [validator repo](https://github.com/validator-labs/validator/blob/86457a3b47efbf05bb6380589b45c35e62fe70fa/chart/validator/templates/cleanup.yaml#L103).
