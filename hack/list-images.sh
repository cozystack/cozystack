#!/bin/sh

for node in 11 12 13; do
  talosctl -n 192.168.123.${node} -e 192.168.123.${node} images ls >> ./hosttmp/images-${SANDBOX_NAME}.txt
  talosctl -n 192.168.123.${node} -e 192.168.123.${node} images --namespace system ls >> ./hosttmp/images-${SANDBOX_NAME}.txt
done
