.PHONY: build run clean test

BINARY := httpmux
BUILD_DIR := ./bin
CONFIG := httpmux.yaml

build:
	go build -o $(BUILD_DIR)/$(BINARY) ./cmd/httpmux

run: build
	$(BUILD_DIR)/$(BINARY) -config $(CONFIG)

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)

# Generate a bcrypt hash for a password (usage: make hash PASS=yourpassword)
hash:
	@go run -mod=mod golang.org/x/crypto/bcrypt/... <<< "$(PASS)" 2>/dev/null || \
		echo "$$( htpasswd -nbBC 10 '' '$(PASS)' | cut -d: -f2 )"
