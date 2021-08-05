#!/bin/sh

if [ -z "$CONFIG_PATH" ]; then
    echo "Config path is required!"
    exit 1
fi

/app/dione -config "$CONFIG_PATH"