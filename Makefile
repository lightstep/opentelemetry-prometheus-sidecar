# Copyright 2015 The Prometheus Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

GO                ?= go
GOFMT             ?= $(GO)fmt
PKGS              ?= ./...
DOCKER_IMAGE_NAME ?= opentelemetry-prometheus-sidecar
DOCKER_IMAGE_TAG  ?= $(subst /,-,$(shell git rev-parse --abbrev-ref HEAD))

all: build

deps:
	@echo ">> getting dependencies"
	$(GO) mod download

test-short:
	@echo ">> running short tests"
	$(GO) test -short $(GOOPTS) ${PKGS}

test: deps
	@echo ">> running all tests"
	@$(GO) test $(GOOPTS) ${PKGS}
	$(GO) test -race $(GOOPTS) ${PKGS}

format:
	@echo ">> formatting code"
	$(GOFMT) -l -w ${PKGS}

vet:
	@echo ">> vetting code"
	@$(GO) vet $(GOOPTS) ${PKGS}

build: test
	@echo ">> building binaries"
	$(GO) build $(GOOPTS) ${PKGS}

docker:
	@echo ">> building docker image"
	docker build -t "$(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)" .

.PHONY: all deps test-short test format vet build vet docker
