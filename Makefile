.PHONY: build web test demo

build: web
	go build -o bin/substrate-console ./cmd/substrate-console

web:
	cd web && npm ci && npm run build

test:
	go vet ./... && go test ./...
	cd web && npx tsc --noEmit -p .

demo: build
	bin/substrate-console serve --demo
