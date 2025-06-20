#!/bin/sh

rm ./hosttmp/images.txt

for node in 11 12 13; do
  talosctl -n 192.168.123.${node} -e 192.168.123.${node} images ls >> ./hosttmp/images.txt
  talosctl -n 192.168.123.${node} -e 192.168.123.${node} images --namespace system ls >> ./hosttmp/images.txt
done
