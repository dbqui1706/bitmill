.PHONY: run build test tidy clean

APP_NAME = streaming-server

run:
	@go run cmd/server/main.go

build:
	@mkdir -p bin
	@go build -o bin/$(APP_NAME) cmd/server/main.go
	@echo "Build completed: bin/$(APP_NAME)"

tidy:
	@go mod tidy

clean:
	@rm -rf bin
	@echo "Cleaned build artifacts."
