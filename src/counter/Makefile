.PHONY: build-docker
build-docker:
	@echo "Building Docker image for counter application..."
	docker build -t hxy9243/counter-app:latest .

.PHONY: build
build: build-docker

.PHONY: init-helm
init-helm:
	@echo "Updating Helm dependencies..."
	helm dependency update helm/counter-app/

.PHONY: deploy-helm
deploy-helm: init-helm build-docker
	@echo "Deploying counter-app Helm chart..."
	helm upgrade --install counter-app helm/counter-app/

.PHONY: build-docker init-helm deploy-helm
