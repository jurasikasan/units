#!/bin/bash
go install github.com/swaggo/swag/cmd/swag@latest
GO_BIN=$(go env GOPATH)/bin
$GO_BIN/swag init -g cmd/app/main.go
rm -f docs/docs.go
