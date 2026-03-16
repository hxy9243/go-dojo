Distributed Counter Implementation in Go
====

This project is a distributed counter service written in Go. It exposes:

- a write API that accepts counter events over HTTP and publishes them to Kafka
- a consumer that aggregates those events and persists totals in Cassandra
- a read API that serves current counter values, using Redis as a cache in front of Cassandra

The deployment under [`helm/`](./helm) packages the application together with Kafka, Cassandra, Redis Sentinel, and supporting dependencies for running on Kubernetes.

# Overview

The service is split into three workers:

- `write-api`: accepts `POST /event` on port `8080`
- `aggregator`: reads Kafka events and updates Cassandra counters
- `read-api`: serves `GET /counter?key=...` on port `8081`

The high-level flow is:

1. A client sends an event to the write API.
2. The write API publishes the event key and increment value to Kafka.
3. The consumer aggregates events and stores durable totals in Cassandra.
4. The read API returns the current value, using Redis to reduce repeated Cassandra reads.

# Prerequisites

- Go `1.25`
- Docker
- `kubectl`
- Helm `v3`
- Kind

# Quick Start

# Build The Image

Build the container image expected by the chart:

```bash
make build-docker
```

That creates:

```bash
hxy9243/counter-app:latest
```

If you are deploying to Kind, load the image into the cluster:

```bash
make kind-load
```

If you are deploying to another Kubernetes cluster, push the image to a registry and override the chart values if needed:

```bash
helm upgrade --install counter-app helm/counter-app \
  --namespace counter \
  --create-namespace \
  --set image.repository=<your-registry>/counter-app \
  --set image.tag=<your-tag>
```

# Deploy With Helm on KIND

From [`src/counter`](./):

```bash
make kind
make build-docker
helm repo add jetstack https://charts.jetstack.io
helm repo add strimzi https://strimzi.io/charts/
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo add k8ssandra https://helm.k8ssandra.io/stable
helm repo update
make init-helm
make kind-load
helm upgrade --install counter-app helm/counter-app \
  --namespace counter \
  --create-namespace
kubectl get pods -n counter
```

Wait until the application pods and dependency pods are `Running` or `Completed`.

Because the chart uses `LoadBalancer` services and Kind does not provide an external load balancer by default, expose the APIs locally with port-forwarding:

```bash
kubectl port-forward -n counter svc/counter-app-write-api 8080:8080
kubectl port-forward -n counter svc/counter-app-read-api 8081:8081
```

Run those in separate terminals.

## Deploy With Helm

Initialize chart dependencies:

```bash
helm repo add jetstack https://charts.jetstack.io
helm repo add strimzi https://strimzi.io/charts/
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo add k8ssandra https://helm.k8ssandra.io/stable
helm repo update
make init-helm
```

Install or upgrade the release:

```bash
helm upgrade --install counter-app helm/counter-app \
  --namespace counter \
  --create-namespace
```

Check rollout state:

```bash
kubectl get all -n counter
kubectl get pods -n counter
```

# Testing the Application

## 1. Send a counter event

```bash
curl -i http://localhost:8080/event \
  -H 'Content-Type: application/json' \
  -d '{
    "id": "demo-1",
    "timestamp": "2026-03-16T12:00:00Z",
    "eventkey": "page-views",
    "eventvalue": 3
  }'
```

Expected result:

- HTTP status `202 Accepted`

## 2. Read the current value

```bash
curl -i "http://localhost:8081/counter?key=page-views"
```

Expected JSON shape:

```json
{
  "key": "page-views",
  "value": 3
}
```

## 3. Validate eventual aggregation

The consumer updates Cassandra asynchronously, so the value may not be visible immediately after the write. Poll until the expected value appears:

```bash
while true; do
  curl -s "http://localhost:8081/counter?key=page-views"
  echo
  sleep 1
done
```

To validate accumulation, send another event for the same key:

```bash
curl -i http://localhost:8080/event \
  -H 'Content-Type: application/json' \
  -d '{
    "id": "demo-2",
    "timestamp": "2026-03-16T12:00:05Z",
    "eventkey": "page-views",
    "eventvalue": 2
  }'
```

Then query again:

```bash
curl -s "http://localhost:8081/counter?key=page-views"
```

The final value should eventually become `5`.

## Remove the release:

```bash
helm uninstall counter-app -n counter
```


## Notes

- `POST /event` only accepts `POST`; `GET /counter` only accepts `GET`.
- `GET /counter` requires the `key` query parameter.
- The read path is cached in Redis, so repeated reads for the same key should be faster after the first lookup.
