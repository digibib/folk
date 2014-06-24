all: build

clean:
	@go clean
	@rm -f folk.tar.gz
	@rm -f folk

deps:
	@go get -d -v ./...
	@wget http://necolas.github.com/normalize.css/3.0.1/normalize.css -O data/public/normalize.css
	@wget http://cdn.ractivejs.org/edge/ractive.min.js -O data/public/ractive.js
	@wget https://raw.github.com/ractivejs/ractive-events-keys/master/ractive-events-keys.js -O data/public/ractive-events-keys.js


build: deps
	@export GOBIN=$(shell pwd)
	@go build

package: build
	@tar -cvzf folk.tar.gz folk data/

test:
	@go test ./...

run:
	@go run folk.go api.go
