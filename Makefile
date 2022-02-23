# ##
#
# Run 'make help' for a summary
#
# ##

# Binaries
BIN         := func
BIN_DARWIN  ?= $(BIN)_darwin_amd64
BIN_LINUX   ?= $(BIN)_linux_amd64
BIN_WINDOWS ?= $(BIN)_windows_amd64.exe

# Version
# A verbose version is built into the binary including a date stamp, git commit
# hash and the version tag of the current commit (semver) if it exists.
# If the current commit does not have a semver tag, 'tip' is used, unless there
# is a TAG environment variable. Precedence is git tag, environment variable, 'tip'
DATE    := $(shell date -u +"%Y%m%dT%H%M%SZ")
HASH    := $(shell git rev-parse --short HEAD 2>/dev/null)
VTAG    := $(shell git tag --points-at HEAD)
VTAG    := $(shell [ -z $(VTAG) ] && echo $(ETAG) || echo $(VTAG))
VERS    ?= $(shell [ -z $(VTAG) ] && echo 'tip' || echo $(VTAG) )
LDFLAGS := "-X main.date=$(DATE) -X main.vers=$(VERS) -X main.hash=$(HASH)"

# All Code prerequisites, including generated files, etc.
CODE := $(shell find . -name '*.go') pkged.go go.mod schema/func_yaml-schema.json
TEMPLATES := $(shell find templates -name '*' -type f)

.PHONY: test

# Default Targets
all: build

# Help Text
# Headings: lines with `##$` comment prefix
# Targets:  printed if their line includes a `##` comment
help:
	@echo 'Usage: make <OPTIONS> ... <TARGETS>'
	@echo ''
	@echo 'Available targets are:'
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)


###############
##@ Development
###############

build: $(BIN) ## (default) Build binary for current OS

$(BIN): $(CODE)
	env CGO_ENABLED=0 go build -ldflags $(LDFLAGS) ./cmd/$(BIN)

test: $(CODE) ## Run core unit tests
	go test -v ./

check: bin/golangci-lint ## Check code quality (lint)
	./bin/golangci-lint run --timeout 300s
	cd test/_e2e && ../../bin/golangci-lint run --timeout 300s

bin/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b ./bin v1.43.0

pkged.go: $(TEMPLATES)
	# Removing temporary template files
	@rm -rf templates/node/cloudevents/node_modules
	@rm -rf templates/node/http/node_modules
	@rm -rf templates/python/cloudevents/__pycache__
	@rm -rf templates/python/http/__pycache__
	@rm -rf templates/typescript/cloudevents/node_modules
	@rm -rf templates/typescript/http/node_modules
	@rm -rf templates/rust/cloudevents/target
	@rm -rf templates/rust/http/target
	@rm -rf templates/quarkus/cloudevents/target
	@rm -rf templates/quarkus/http/target
	@rm -rf templates/springboot/cloudevents/target
	@rm -rf templates/springboot/http/target
	# Encoding ./templates as pkged.go
# See ./hack/tools.go which triggers the vendoring of pkger cmd
# The temp file ./hack/package.go averts a "no buildable files" error
# gofmt updates the resultant pkged.go file to the new build tag f ormat.
	@echo "package tools" > hack/package.go
	@go run ./vendor/github.com/markbates/pkger/cmd/pkger
	@rm hack/package.go
	@gofmt -s -w pkged.go


clean: ## Remove generated artifacts such as binaries and schemas
	rm -f $(BIN) $(BIN_WINDOWS) $(BIN_LINUX) $(BIN_DARWIN)
	rm -f schema/func_yaml-schema.json
	rm -f templates/go/cloudevents/go.sum
	rm -f templates/go/http/go.sum
	rm -f coverage.out


#############
##@ Templates
#############

test-templates: test-go test-node test-python test-quarkus test-rust test-typescript ## Run all template tests

test-go: ## Test Go templates
	cd templates/go/cloudevents && go mod tidy && go test
	cd templates/go/http && go mod tidy && go test

test-node: ## Test Node templates
	cd templates/node/cloudevents && npm ci && npm test && rm -rf node_modules
	cd templates/node/http && npm ci && npm test && rm -rf node_modules

test-python: ## Test Python templates
	cd templates/python/cloudevents && pip3 install -r requirements.txt && python3 test_func.py && rm -rf __pycache__
	cd templates/python/http && python3 test_func.py && rm -rf __pycache__

test-quarkus: ## Test Quarkus templates
	cd templates/quarkus/cloudevents && mvn test && mvn clean
	cd templates/quarkus/http && mvn test && mvn clean

test-rust: ## Test Rust templates
	cd templates/rust/cloudevents && cargo test && cargo clean
	cd templates/rust/http && cargo test && cargo clean

test-typescript: ## Test Typescript templates
	cd templates/typescript/cloudevents && npm ci && npm test && rm -rf node_modules build
	cd templates/typescript/http && npm ci && npm test && rm -rf node_modules build


###################
##@ Extended Testing (cluster required)
###################

test-integration: ## Run integration tests using an available cluster.
	go test -tags integration ./... -v

test-e2e: ## Run end-to-end tests using an available cluster.
	./test/e2e_lifecycle_tests.sh node
	./test/e2e_extended_tests.sh


######################
##@ Release Artifacts
######################

cross-platform: darwin linux windows ## Build all distributable (cross-platform) binaries

darwin: $(BIN_DARWIN) ## Build for Darwin (macOS)

$(BIN_DARWIN): pkged.go
	env CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o $(BIN_DARWIN) -ldflags $(LDFLAGS) ./cmd/$(BIN)

linux: $(BIN_LINUX) ## Build for Linux

$(BIN_LINUX): pkged.go
	env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(BIN_LINUX) -ldflags $(LDFLAGS) ./cmd/$(BIN)

windows: $(BIN_WINDOWS) ## Build for Windows

$(BIN_WINDOWS): pkged.go
	env CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o $(BIN_WINDOWS) -ldflags $(LDFLAGS) ./cmd/$(BIN)

######################
##@ Schemas
######################
schema-generate: schema/func_yaml-schema.json ## Generate func.yaml schema
schema/func_yaml-schema.json: function.go
	go run schema/generator/main.go

schema-check: ## Check that func.yaml schema is up-to-date
	mv schema/func_yaml-schema.json schema/func_yaml-schema-previous.json
	make schema-generate
	diff schema/func_yaml-schema.json schema/func_yaml-schema-previous.json ||\
	(echo "\n\nFunction config schema 'schema/func_yaml-schema.json' is obsolete, please run 'make schema-generate'.\n\n"; rm -rf schema/func_yaml-schema-previous.json; exit 1)
	rm -rf schema/func_yaml-schema-previous.json

