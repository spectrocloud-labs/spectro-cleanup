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
  # multus files we want to delete
  file-config.json: |-
    [
      "/host/etc/cni/net.d/00-multus.conf",
      "/host/opt/cni/bin/multus"
    ]
  # Kubernetes resources we want to delete.
  #
  # The first entry deletes all secrets in the cluster
  # to illustrate that name and namespace are optional.
  # You would likely not want to do that in production :-)
  #
  # Note: the spectro-cleanup workload (this DaemonSet) and its RBAC are
  # handled by the --self-gvr/--self-name/--self-namespace flags on the
  # container, not by entries in resource-config.json.
  resource-config.json: |-
    [
      {
        "group": "",
        "version": "v1",
        "resource": "secrets"
      },
      {
        "group": "",
        "version": "v1",
        "resource": "configmaps",
        "name": "spectro-cleanup-config",
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
        args:
        - --self-gvr=apps/v1/daemonsets
        - --self-name=spectro-cleanup
        - --self-namespace=kube-system
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
  # spectro-cleanup resources we want to delete.
  #
  # Note: the spectro-cleanup workload (this Job) and its RBAC are
  # handled by the --self-gvr/--self-name/--self-namespace flags on the
  # container, not by entries in resource-config.json.
  resource-config.json: |-
    [
      {
        "group": "",
        "version": "v1",
        "resource": "configmaps",
        "name": "spectro-cleanup-config",
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
        - --self-gvr=batch/v1/jobs
        - --self-name=spectro-cleanup
        - --self-namespace=kube-system
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

#### Self-cleanup

When you want spectro-cleanup to delete its own workload after it finishes processing `resource-config.json`, pass the three `--self-*` flags to the container:

| Flag | Purpose | Example |
|------|---------|---------|
| `--self-gvr` | GroupVersionResource of the cleanup workload, formatted as `group/version/resource`. Use an empty group segment for core resources (e.g. `/v1/pods`). | `batch/v1/jobs` |
| `--self-name` | Metadata name of the cleanup workload. When set, self-cleanup is enabled. | `spectro-cleanup` |
| `--self-namespace` | Namespace of the cleanup workload. Leave empty only when the target is cluster-scoped. | `kube-system` |

With the flags set, after the main resource list is processed spectro-cleanup will:

1. Get the self target and attach it as an `ownerReference` to the cleanup `ServiceAccount`, plus either (`Role`,`RoleBinding`) or (`ClusterRole`,`ClusterRoleBinding`) depending on which `--*-name` flags you also pass.
2. Delete the self target. Kubernetes garbage collection then reaps the RBAC resources via the owner references.

If `--self-name` is **not** set, self-cleanup is skipped entirely. The cleanup workload itself is expected to be garbage collected by whatever deployed it (for example, a Helm chart with `helm.sh/hook-delete-policy: "hook-failed,hook-succeeded"` on a pre-delete hook Job).

> **Migration note:** previous versions required the spectro-cleanup `configmap` and `daemonset/job/pod` to appear as the **last entries** in `resource-config.json`. That implicit contract is gone. Existing configs that still list the cleanup workload as an entry will treat it as an ordinary delete (the implicit owner-reference wiring is no longer performed). Move the workload identity into `--self-*` flags and remove it from `resource-config.json`.

#### Blocking deletion

By default, delete operations for kubernetes resources are blocking. The cleanup process will poll until the resource is fully removed from the API server. You can customize this behaviour via `--blocking-deletion` (defaults to `true`). The polling interval and timeout default to 2s and 5m, respectively, and be customized via `--deletion-interval-seconds`, and `--deletion-timeout-seconds`.

#### Non-blocking self-cleanup via gRPC

You can optionally configure a gRPC server to run as a part of spectro-cleanup. This server has a single endpoint, `FinalizeCleanup`.
When this server is configured **and self-cleanup is enabled via the `--self-*` flags**, spectro-cleanup will wait for a `FinalizeCleanup` request before deleting itself.
In this case, the `--cleanup-timeout-seconds` flag is the fallback time to self destruct if the `FinalizeCleanup` request never arrives.

The gRPC server has no effect when self-cleanup is disabled: if `--self-name` is unset, spectro-cleanup exits immediately after processing `resource-config.json`.

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
        - --self-gvr=batch/v1/jobs
        - --self-name=validator-cleanup
        - --self-namespace={{ .Release.Namespace }}
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

If you'd like to cleanup cluster scoped resources, you'll need to provide both the `--cluster-role-name` and `--cluster-role-binding-name` flags.
Otherwise, spectro-cleanup will operate using the default or provided `--role-name` and `--role-binding-name` values.
Below is an example of a valid `ClusterRole` and `ClusterRoleBinding` for this configuration.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: spectro-cleanup-role
rules:
- apiGroups:
  - admissionregistration.k8s.io
  resources:
  - validatingwebhookconfigurations
  - mutatingwebhookconfigurations
  verbs:
  - get
  - delete
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
  - roles
  - rolebindings
  - clusterroles
  - clusterrolebindings
  verbs:
  - '*'
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: spectro-cleanup-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: spectro-cleanup-role
subjects:
  - kind: ServiceAccount
    name: spectro-cleanup
    namespace: kube-system
```
