#!/bin/sh

# image-refs.txt is what the release's runtime multi-arch audit checks, so it
# is written only when every node answered: a list missing a node would pass
# the images only that node pulled.
rm -f images.tmp image-refs.txt
complete=1
for node in 11 12 13; do
  talosctl -n 192.168.123.${node} -e 192.168.123.${node} images ls >> images.tmp || complete=0
  talosctl -n 192.168.123.${node} -e 192.168.123.${node} images --namespace system ls >> images.tmp || complete=0
done

while read _ name sha _ ; do echo $sha $name ; done < images.tmp | sort -u > images.txt

# <repo>@<digest>, the digest being what the node's pull resolved to: an index
# for a multi-arch image. A name that is itself a digest is an image ID alias
# and names no repository.
if [ "$complete" != 1 ]; then
  echo "collect-images: a node could not be read; image-refs.txt not written" >&2
  exit 0
fi
awk '$3 ~ /^sha256:/ && $2 !~ /^sha256:/ {
  name = $2
  sub(/@.*/, "", name)
  if (match(name, /:[^\/]*$/)) name = substr(name, 1, RSTART - 1)
  print name "@" $3
}' images.tmp | sort -u > image-refs.txt
