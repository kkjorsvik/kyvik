.PHONY: all build build-sandbox build-all run test test-race clean dev install install-service
.PHONY: regen-service uninstall upgrade lint fmt migrate docker-build docker-run generate-key

BINARY := kyvik
SANDBOX_BINARY := kyvik-sandbox
VERSION := $(shell date -u +%Y.%m.%d)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_DIR := ./build
INSTALL_DIR := /opt/kyvik/bin
SERVICE := kyvik.service
GO_FLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

all: fmt lint test build-all

build:
	@echo "Building $(BINARY) v$(VERSION) ($(COMMIT))..."
	@mkdir -p $(BUILD_DIR)
	go build $(GO_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/kyvik

build-sandbox:
	@echo "Building $(SANDBOX_BINARY)..."
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(SANDBOX_BINARY) ./cmd/kyvik-sandbox

build-all: build build-sandbox

install: build-all
	@mkdir -p bin
	@cp $(BUILD_DIR)/$(BINARY) bin/$(BINARY)
	@cp $(BUILD_DIR)/$(SANDBOX_BINARY) bin/$(SANDBOX_BINARY)
	@sudo bash deploy/setup.sh

install-service: build-all
	@if [ $$(id -u) -ne 0 ]; then echo "Error: must run as root (sudo make install-service)"; exit 1; fi
	systemctl stop $(SERVICE) || true
	mkdir -p $(INSTALL_DIR)
	cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	cp $(BUILD_DIR)/$(SANDBOX_BINARY) $(INSTALL_DIR)/$(SANDBOX_BINARY)
	bash deploy/systemd/generate-service.sh
	systemctl daemon-reload
	systemctl restart $(SERVICE)

regen-service:
	@if [ $$(id -u) -ne 0 ]; then echo "Error: must run as root (sudo make regen-service)"; exit 1; fi
	bash deploy/systemd/generate-service.sh
	systemctl daemon-reload
	systemctl restart $(SERVICE)

uninstall:
	@if [ $$(id -u) -ne 0 ]; then echo "Error: must run as root (sudo make uninstall)"; exit 1; fi
	systemctl stop $(SERVICE) || true
	systemctl disable $(SERVICE) || true
	rm -f /etc/systemd/system/$(SERVICE)
	systemctl daemon-reload
	rm -f $(INSTALL_DIR)/$(BINARY) $(INSTALL_DIR)/$(SANDBOX_BINARY)
	rm -f /usr/local/bin/kv
	@echo "Uninstalled. Config (/etc/kyvik) and data (/var/lib/kyvik) preserved."
	@echo "To remove all data: sudo rm -rf /etc/kyvik /var/lib/kyvik /var/log/kyvik /opt/kyvik"

upgrade: build-all
	@mkdir -p bin
	@cp $(BUILD_DIR)/$(BINARY) bin/$(BINARY)
	@cp $(BUILD_DIR)/$(SANDBOX_BINARY) bin/$(SANDBOX_BINARY)
	@sudo bash deploy/setup.sh --upgrade

run: build
	$(BUILD_DIR)/$(BINARY)

test:
	go test -p 1 -timeout 600s ./...

test-race:
	go test -race -p 1 -timeout 600s ./...

clean:
	rm -rf $(BUILD_DIR)

dev:
	@echo "Starting development mode..."
	go run ./cmd/kyvik

fmt:
	gofmt -w .

lint:
	golangci-lint run ./...

# Database
migrate:
	@echo "Migrations are applied automatically on startup for PostgreSQL."
	@echo "Use 'kyvik migrate' subcommands only for explicit migration workflows."

# Docker
docker-build:
	docker build -t $(BINARY):$(VERSION) .

docker-run:
	docker run -p 8080:8080 -v kyvik-data:/app/data $(BINARY):$(VERSION)

# Secrets
generate-key:
	@if [ -f /etc/kyvik/env ] && grep -q 'KYVIK_MASTER_KEY=' /etc/kyvik/env; then \
		echo "Key already exists in /etc/kyvik/env — skipping."; \
	else \
		sudo mkdir -p /etc/kyvik; \
		echo "KYVIK_MASTER_KEY=$$(openssl rand -base64 32)" | sudo tee /etc/kyvik/env > /dev/null; \
		sudo chmod 600 /etc/kyvik/env; \
		echo "Master key written to /etc/kyvik/env"; \
	fi
