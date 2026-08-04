APP_NAME := collyrobot
BIN_DIR := bin
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
EXE_EXT := $(if $(filter windows,$(GOOS)),.exe,)
OUTPUT := $(BIN_DIR)/$(APP_NAME)$(EXE_EXT)

.PHONY: all build run tidy clean

all: build

build:
	@mkdir -p "$(BIN_DIR)"
	go build -trimpath -buildvcs=false -o "$(OUTPUT)" ./cmd/server
	@echo "Built $(OUTPUT)"

run: build
	"$(OUTPUT)"

tidy:
	go mod tidy

clean:
	$(RM) -r "$(BIN_DIR)"
