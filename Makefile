.PHONY: generate
generate:
	@echo "Generating public APIs..."
	@go run ./cmd/orlop-gen
	@echo "Running controller-gen for public APIs..."
	@controller-gen object:headerFile=hack/boilerplate.go.txt paths="./apis/public/..."
	@echo "Done!"

.PHONY: clean
clean:
	@echo "Cleaning generated files..."
	@rm -rf apis/public
	@echo "Done!"

.PHONY: test
test:
	@echo "Testing generator..."
	@go test ./pkg/generator/...
	@echo "Done!"
