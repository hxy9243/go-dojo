#!/bin/bash

# This script automates the installation of the counter-app Helm chart.

# Exit immediately if a command exits with a non-zero status.
set -e

# Variables
RELEASE_NAME="counter-app-release"
CHART_PATH="./counter-app"
NAMESPACE="default"

# 1. Add the Bitnami Helm repository
echo "Adding Bitnami Helm repository..."
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo update

# 2. Update Helm dependencies
echo "Updating Helm dependencies for the chart..."
helm dependency update "$CHART_PATH"

# 3. Install the Helm chart
echo "Installing the Helm chart..."
helm install "$RELEASE_NAME" "$CHART_PATH" --namespace "$NAMESPACE" --create-namespace

echo "Helm chart installed successfully!"
echo "Release name: $RELEASE_NAME"
echo "Namespace: $NAMESPACE"

# Instructions to check the status
echo "You can check the status of the deployment by running:"
echo "kubectl get all -n $NAMESPACE"
