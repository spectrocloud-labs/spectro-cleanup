[![Contributions Welcome](https://img.shields.io/badge/contributions-welcome-brightgreen.svg?style=flat)](https://github.com/spectrocloud-labs/spectro-cleanup/issues)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![codecov](https://codecov.io/github/spectrocloud-labs/spectro-cleanup/graph/badge.svg?token=Q15XUCRNCN)](https://codecov.io/github/spectrocloud-labs/spectro-cleanup)
[![Go Reference](https://pkg.go.dev/badge/github.com/spectrocloud-labs/spectro-cleanup.svg)](https://pkg.go.dev/github.com/spectrocloud-labs/spectro-cleanup)

# spectro-cleanup
A generic cleanup utility for removing arbitrary files from nodes and/or resources from a K8s cluster. Can be deployed as a DaemonSet/Job/Pod.

## Examples
### DaemonSet Configuration

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

### Job Configuration
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
        env:
        - name: CLEANUP_DELAY_SECONDS
          value: "10"
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
