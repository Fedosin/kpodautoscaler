# KPodAutoscaler Helm Chart

This Helm chart deploys the KPodAutoscaler controller on a Kubernetes cluster.

## Prerequisites

- Kubernetes 1.16+
- Helm 3.0+
- kubectl configured to access your cluster

## Installation

### Add the Helm repository (if published)

```bash
helm repo add kpodautoscaler https://yourusername.github.io/kpodautoscaler
helm repo update
```

### Install from local chart

```bash
# Install with default values
helm install kpodautoscaler ./helm/kpodautoscaler \
  --namespace kpodautoscaler-system \
  --create-namespace

# Install with custom values
helm install kpodautoscaler ./helm/kpodautoscaler \
  --namespace kpodautoscaler-system \
  --create-namespace \
  --set image.repository=myregistry/kpodautoscaler \
  --set image.tag=v0.1.0
```

## Configuration

The following table lists the configurable parameters of the KPodAutoscaler chart and their default values.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of controller replicas | `1` |
| `image.repository` | Controller image repository | `controller` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Image tag (defaults to chart appVersion) | `""` |
| `imagePullSecrets` | Image pull secrets | `[]` |
| `nameOverride` | Override the chart name | `""` |
| `fullnameOverride` | Override the full name | `""` |
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.annotations` | Service account annotations | `{}` |
| `serviceAccount.name` | Service account name | `""` |
| `podAnnotations` | Pod annotations | `{}` |
| `podSecurityContext` | Pod security context | See values.yaml |
| `securityContext` | Container security context | See values.yaml |
| `resources` | CPU/Memory resource requests/limits | See values.yaml |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |
| `controller.leaderElection.enabled` | Enable leader election | `true` |
| `controller.metrics.enabled` | Enable metrics endpoint | `true` |
| `controller.metrics.bindAddress` | Metrics bind address | `:8080` |
| `controller.webhook.enabled` | Enable webhook | `false` |
| `monitoring.serviceMonitor.enabled` | Create ServiceMonitor for Prometheus | `false` |
| `rbac.create` | Create RBAC resources | `true` |
| `crds.install` | Install CRDs | `true` |
| `crds.keep` | Keep CRDs on chart uninstall | `true` |

### Using a custom values file

Create a `values-custom.yaml` file:

```yaml
image:
  repository: myregistry/kpodautoscaler
  tag: v0.1.0

resources:
  limits:
    cpu: 200m
    memory: 256Mi
  requests:
    cpu: 100m
    memory: 128Mi

monitoring:
  serviceMonitor:
    enabled: true
    labels:
      prometheus: kube-prometheus
```

Install with custom values:

```bash
helm install kpodautoscaler ./helm/kpodautoscaler \
  --namespace kpodautoscaler-system \
  --create-namespace \
  --values values-custom.yaml
```

## Upgrade

```bash
helm upgrade kpodautoscaler ./helm/kpodautoscaler \
  --namespace kpodautoscaler-system
```

## Uninstall

```bash
helm uninstall kpodautoscaler --namespace kpodautoscaler-system
```

By default, CRDs are kept when uninstalling the chart. To remove CRDs:

```bash
kubectl delete crd kpodautoscalers.autoscaling.kpodautoscaler.io
```

## Troubleshooting

### Check controller logs

```bash
kubectl logs -n kpodautoscaler-system -l app.kubernetes.io/name=kpodautoscaler
```

### Verify CRDs are installed

```bash
kubectl get crd kpodautoscalers.autoscaling.kpodautoscaler.io
```

### Check controller status

```bash
kubectl get deployment -n kpodautoscaler-system -l app.kubernetes.io/name=kpodautoscaler
kubectl get pods -n kpodautoscaler-system -l app.kubernetes.io/name=kpodautoscaler
``` 