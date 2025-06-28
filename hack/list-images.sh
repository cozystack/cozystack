#!/bin/sh

for node in 11 12 13; do
  talosctl -n 192.168.123.${node} -e 192.168.123.${node} images ls >> /workspace/images.tmp
  talosctl -n 192.168.123.${node} -e 192.168.123.${node} images --namespace system ls >> /workspace/images.tmp
done

while read _ name sha _ ; do echo $sha $name ; done < /workspace/images.tmp | sort -u > /workspace/images.txt
