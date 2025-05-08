update:
	rm -rf charts
	helm repo add beryju https://charts.beryju.io
	helm repo update beryju
	helm pull beryju/guacamole --untar --untardir charts
