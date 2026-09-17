.PHONY: test coverage format lint clean

test:
	go test -count=1 ./...

coverage:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

format:
	gofumpt -l -w .

lint:
	golangci-lint run

clean:
	rm -f coverage.out
