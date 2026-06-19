SHELL := /usr/bin/env bash

ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)

PLATFORM ?=
OUT_DIR ?= $(ROOT)/build/tmp/bin
OUT ?= $(OUT_DIR)/nexus

.PHONY: build clean info

build:
	@mkdir -p "$(OUT_DIR)"
	@set -euo pipefail; \
	source "$(ROOT)/build/platform.sh"; \
	PLATFORM="$(PLATFORM)" nq_platform_resolve; \
	cd "$(ROOT)/nexus"; \
	CGO_ENABLED=0 \
	  $${NEXUS_GOOS:+GOOS=$${NEXUS_GOOS}} \
	  $${NEXUS_GOARCH:+GOARCH=$${NEXUS_GOARCH}} \
	  $${NEXUS_GOARM:+GOARM=$${NEXUS_GOARM}} \
	  go build -o "$(OUT)" .
	@chmod +x "$(OUT)" || true
	@echo "Built nexus: $(OUT)"

clean:
	@rm -f "$(OUT)"

info:
	@set -euo pipefail; \
	source "$(ROOT)/build/platform.sh"; \
	PLATFORM="$(PLATFORM)" nq_platform_resolve; \
	echo "PLATFORM=$(PLATFORM)"; \
	echo "OUT=$(OUT)"; \
	echo "GOOS=$${NEXUS_GOOS:-} GOARCH=$${NEXUS_GOARCH:-} GOARM=$${NEXUS_GOARM:-}"; \
	go version
