all: build

clean:
	@go clean
	@rm -f folk.tar.gz
	@rm -f folk
	@rm -f coverage.out

deps:
	@go get -d -v ./...
	@wget http://necolas.github.com/normalize.css/3.0.1/normalize.css -O data/public/normalize.css
	@wget http://cdn.ractivejs.org/edge/ractive.min.js -O data/public/ractive.js

build: deps
	@export GOBIN=$(shell pwd)
	@go build

package: build
	@tar -cvzf folk.tar.gz folk data/

test: deps
	@go test ./...

cover:
	@go test -coverprofile=coverage.out
	@go tool cover -html=coverage.out

run:
	@go run folk.go api.go
