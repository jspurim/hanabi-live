#!/bin/bash

set -e # Exit on any errors
set -x # Enable debugging

# Get the directory of this script
# https://stackoverflow.com/questions/59895/getting-the-source-directory-of-a-bash-script-from-within
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

# Change to the parent directory (the repository root)
DIR="$DIR/.."
cd "$DIR"

# Ensure that the ".env" file exists
if [[ ! -f "$DIR/.env" ]]; then
  cp "$DIR/.env_template" "$DIR/.env"
fi

# Install the JavaScript/TypeScript dependencies and build the client
cd "$DIR/packages/hanabi-client"
npm ci
cd "$DIR"
"$DIR/packages/hanabi-client/build_client.sh"

# Build the server, which will automatically install the Golang dependencies
"$DIR/server/build_server.sh"

echo "Successfully installed dependencies."
