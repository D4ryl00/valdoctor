.PHONY: install
install:
	go install .

.PHONY: build
build:
	go build -o build/valdoctor .

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: test
test:
	go test ./...
