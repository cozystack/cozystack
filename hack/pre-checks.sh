#!/bin/bash

YQ_VERSION="v4.35.1"
RED='\033[31m'
RESET='\033[0m'

check-yq-version() {
    current_version=$(yq -V | awk '$(NF-1) == "version" {print $NF}')
    if [ -z "$current_version" ]; then
        echo "yq is not installed or version cannot be determined."
        exit 1
    fi
    echo "Current yq version: $current_version"

    if [ "$(printf '%s\n' "$YQ_VERSION" "$current_version" | sort -V | head -n1)" = "$YQ_VERSION" ]; then
        echo "Greater than or equal to $YQ_VERSION"
    else
        echo -e "${RED}ERROR: yq version less than $YQ_VERSION${RESET}"
        exit 1
    fi
}

check-yq-version
