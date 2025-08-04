NAME=ingress

include ../../../scripts/package.mk

update: get-cloudflare-ips

get-cloudflare-ips:
	printf '{{- define "ingress.cloudflare-ips" -}}\n%s,%s\n{{- end }}\n' "$$(curl -s https://www.cloudflare.com/ips-v4/ | tr '\n' ,)" "$$(curl -s https://www.cloudflare.com/ips-v6/ | tr '\n' ,)" > templates/_cloudflare-ips.tpl

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
