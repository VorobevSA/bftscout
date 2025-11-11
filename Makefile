SHELL := /bin/bash
BIN ?= bin/bftscout
PKG ?= ./cmd/monitor

.PHONY: help tidy deps build clean run

help:
	@echo "Available targets:"
	@echo "  tidy   - go mod tidy"
	@echo "  deps   - download deps (go mod download)"
	@echo "  build  - build binary to $(BIN) from $(PKG)"
	@echo "  clean  - remove bin/"
	@echo "  run    - run with env from example.env"

tidy:
	go mod tidy

deps:
	go mod download

build: tidy
	mkdir -p bin
	go build -o $(BIN) $(PKG)

clean:
	rm -rf bin

# Run using variables from example.env only (or already exported env)
run:
	go run $(PKG)


