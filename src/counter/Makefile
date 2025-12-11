KIND_CLUSTER_NAME ?= mycluster

.PHONY: build-docker
build-docker:
	@echo "Building Docker image for counter application..."
	docker build -t hxy9243/counter-app:latest .

.PHONY: kind-load
kind-load: build-docker
	@echo "Loading Docker image into Kind cluster '$(KIND_CLUSTER_NAME)'..."
	kind load docker-image hxy9243/counter-app:latest --name $(KIND_CLUSTER_NAME)

.PHONY: build
build: build-docker

.PHONY: init-helm
init-helm:
	@echo "Updating Helm dependencies..."
	helm dependency update helm/counter-app/

.PHONY: deploy-helm
deploy-helm: init-helm kind-load
	@echo "Deploying counter-app Helm chart..."
	helm upgrade --install counter-app helm/counter-app/

.PHONY: build-docker kind-load init-helm deploy-helm
