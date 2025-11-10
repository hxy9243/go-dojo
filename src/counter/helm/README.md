# Counter App Helm Chart

This directory contains a Helm chart to deploy the counter application and its dependencies (Cassandra, Kafka, and Redis).

## Prerequisites

- Kubernetes cluster
- Helm v3

## Chart Structure

- `counter-app/`: The main chart directory.
  - `Chart.yaml`: Defines the chart and its dependencies.
  - `values.yaml`: Default configuration values for the chart.
  - `templates/`: Contains the Kubernetes manifest templates.
    - `read-api.yaml`: Deployment and Service for the read API.
    - `write-api.yaml`: Deployment and Service for the write API.
    - `consumer.yaml`: Deployment for the consumer.
    - `_helpers.tpl`: Helm helper templates.

## How to Install

An `install.sh` script is provided to automate the installation process.

### Automated Installation

To install the chart with its dependencies, run the `install.sh` script:

```bash
bash install.sh
```

This script will:
1. Add the Bitnami Helm repository.
2. Update the chart's dependencies.
3. Install the `counter-app` chart in the `default` namespace.

### Manual Installation

If you prefer to install the chart manually, follow these steps:

1. **Add the Bitnami repository:**
   ```bash
   helm repo add bitnami https://charts.bitnami.com/bitnami
   helm repo update
   ```

2. **Update dependencies:**
   ```bash
   helm dependency update counter-app
   ```

3. **Install the chart:**
   ```bash
   helm install my-release counter-app
   ```

   You can override the default values by creating a custom `values.yaml` file and using the `-f` flag:
   ```bash
   helm install my-release counter-app -f my-values.yaml
   ```

## How to Uninstall

To uninstall the release, run:

```bash
helm uninstall my-release
```
(Replace `my-release` with the actual release name, which is `counter-app-release` if you used the `install.sh` script).
