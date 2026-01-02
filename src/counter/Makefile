KIND_CLUSTER_NAME ?= mycluster
NAMESPACE ?= counter

.PHONY: kind
kind:
	@echo "Building KIND cluster"
	kind create cluster --config ../../configs/kind.yaml

.PHONY: build-docker
build-docker:
	@echo "Building Docker image for counter application..."
	docker build -t hxy9243/counter-app:latest .

.PHONY: kind-load
kind-load: build-docker
	@echo "Loading Docker image into Kind cluster '$(KIND_CLUSTER_NAME)'..."
	kind load docker-image hxy9243/counter-app:latest --name $(KIND_CLUSTER_NAME)

.PHONY: lint
lint:
	@echo "Running golangci-lint..."
	golangci-lint run -v ./...

.PHONY: build
build: build-docker

.PHONY: init-helm
init-helm:
	@echo "Updating Helm dependencies..."
	helm dependency update helm/counter-app/

.PHONY: install-cert-manager
install-cert-manager:
	@echo "Installing cert-manager"
	helm repo add jetstack https://charts.jetstack.io

	# Install the cert-manager helm chart
	helm upgrade --install \
	cert-manager jetstack/cert-manager \
	--namespace $(NAMESPACE) \
	--create-namespace \
	--version v1.19.2 \
	--set crds.enabled=true \
	--wait

.PHONY: deploy-helm
deploy-helm: init-helm kind-load install-cert-manager
	@echo "Deploying counter-app Helm chart..."
	helm upgrade --install counter-app helm/counter-app/

.PHONY: uninstall
uninstall:
	@echo "Uninstalling the cluster"
	helm uninstall cert-manager
	helm uninstall counter-app
