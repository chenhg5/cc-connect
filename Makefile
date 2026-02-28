APP        := cc-connect
MODULE     := github.com/chenhg5/cc-connect
CMD        := ./cmd/cc-connect
DIST       := dist

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -s -w \
  -X main.version=$(VERSION) \
  -X main.commit=$(COMMIT) \
  -X main.buildTime=$(BUILD_TIME)

PLATFORMS := \
  linux/amd64 \
  linux/arm64 \
  darwin/amd64 \
  darwin/arm64 \
  windows/amd64 \
  windows/arm64

.PHONY: build run clean test lint release release-all

build:
	go build -ldflags "$(LDFLAGS)" -o $(APP) $(CMD)

run: build
	./$(APP)

clean:
	rm -f $(APP)
	rm -rf $(DIST)

test:
	go test -v ./...

lint:
	golangci-lint run ./...

release-all: clean
	@mkdir -p $(DIST)
	@$(foreach platform,$(PLATFORMS), \
		$(eval GOOS   := $(word 1,$(subst /, ,$(platform)))) \
		$(eval GOARCH := $(word 2,$(subst /, ,$(platform)))) \
		$(eval EXT    := $(if $(filter windows,$(GOOS)),.exe,)) \
		$(eval OUT    := $(DIST)/$(APP)-$(VERSION)-$(GOOS)-$(GOARCH)$(EXT)) \
		echo "Building $(OUT)" && \
		GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
			go build -ldflags "$(LDFLAGS)" -o $(OUT) $(CMD) && \
	) true
	@echo "Packaging archives..."
	@cd $(DIST) && for f in $(APP)-*; do \
		case "$$f" in \
			*.tar.gz|*.zip) continue ;; \
			*.exe) zip "$${f%.exe}.zip" "$$f" ;; \
			*)     tar czf "$$f.tar.gz" "$$f" ;; \
		esac; \
	done
	@cd $(DIST) && sha256sum * > checksums.txt
	@echo "Done. Binaries and archives in $(DIST)/"

release:
	@if [ -z "$(TARGET)" ]; then \
		echo "Usage: make release TARGET=linux/amd64"; \
		echo "Available: $(PLATFORMS)"; \
		exit 1; \
	fi
	@mkdir -p $(DIST)
	$(eval GOOS   := $(word 1,$(subst /, ,$(TARGET))))
	$(eval GOARCH := $(word 2,$(subst /, ,$(TARGET))))
	$(eval EXT    := $(if $(filter windows,$(GOOS)),.exe,))
	$(eval OUT    := $(DIST)/$(APP)-$(VERSION)-$(GOOS)-$(GOARCH)$(EXT))
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
		go build -ldflags "$(LDFLAGS)" -o $(OUT) $(CMD)
	@echo "Built: $(OUT)"
