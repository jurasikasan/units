#!/bin/bash
# This script generates the Swagger/OpenAPI documentation for the threads service.
# It scans the source code for 'swag' annotations and creates a 'docs' directory
# containing the generated swagger.json, swagger.yaml, and docs.go files.

# The -g flag points to the main.go file where the general API information is defined.
echo "Installing github.com/swaggo/swag/cmd/swag"
go install github.com/swaggo/swag/cmd/swag@latest
GO_BIN=$(go env GOPATH)/bin
echo "Generating Swagger documentation for threads service..."
$GO_BIN/swag init -g cmd/app/main.go
rm -f docs/docs.go
echo "Done. Documentation generated in the 'docs' directory."
